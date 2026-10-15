package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JFRabbit/serialcat/internal/agent"
	"github.com/JFRabbit/serialcat/pkg/serial"
)

// agentCmd 是 Windows 串口代理子命令（在带串口的 Windows 机器上运行）。
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "串口代理（目前仅 Windows 支持，其它系统是否支持待定）",
	Args:  cobra.NoArgs,
}

// agentServeCmd 启动串口 HTTP 代理服务。
var agentServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "启动串口代理服务",
	Long: "在插着串口这台机器上运行：自动连接串口（未指定时自动选择），并把串口数据通过网络提供给" +
		"其它电脑的 serialcat 查看。\n" +
		"启动后会打印本机可访问地址；配合 --enable-write 可在对端下发命令。\n" +
		"参数：--listen HTTP 监听地址(默认 :7100)；--port 串口名(如 COM5，不填自动选)；" +
		"--baud 波特率(默认 115200)；--enable-write 开启下行写；--token 固定写令牌(不填则随机)；" +
		"--no-local-log 关闭本地日志落盘；--out-dir 本地日志落盘目录（默认运行目录下 logs/）。",
	Args: cobra.NoArgs,
	RunE: runAgentServe,
}

var (
	serveListen      string
	servePort        string
	serveBaud        int
	serveEnableWrite bool
	serveToken       string
	serveNoLocalLog  bool
	serveOutDir      string
)

// agentProbeCmd 探测串口可用性与波特率。
var agentProbeCmd = &cobra.Command{
	Use:   "probe [port]",
	Short: "查看或探测串口",
	Long: "不带参数时列出本机所有串口；带一个串口名时测试它，并给出最推荐的波特率。\n" +
		"参数：<port> 串口名（如 COM5），可省略。",
	Args: cobra.MaximumNArgs(1),
	RunE: runAgentProbe,
}

func runAgentServe(cmd *cobra.Command, _ []string) error {
	drv, err := serial.NewDriver()
	if err != nil {
		return err
	}

	// 端口未明确指定时，自动探测并选择串口：
	//   无串口 → 报错；唯一 → 直接连；多个 → 控制台交互选择。
	port := servePort
	if port == "" {
		chosen, err := selectPort(drv)
		if err != nil {
			return err
		}
		port = chosen
	}

	// --enable-write：token 取自 --token（固定/不变）或每次随生成的随机值（控制台输出，不落文件）。
	opts := agent.Options{}
	if serveEnableWrite {
		opts.WriteEnabled = true
		if serveToken != "" {
			opts.WriteToken = serveToken
		} else {
			opts.WriteToken = agent.RandomToken(16)
		}
	}
	opts.NoLocalLog = serveNoLocalLog
	opts.OutDir = serveOutDir

	srv, err := agent.NewServer(serveListen, port, serveBaud, drv, opts)
	if err != nil {
		return err
	}

	// 监听地址端口：若已被占用自动 +1，直到绑定成功或超过最大尝试次数。
	ln, addr, err := listenAutoIncrement(serveListen)
	if err != nil {
		return err
	}
	defer ln.Close()
	if addr != serveListen {
		fmt.Printf("端口 %s 被占用，改用 %s\n", serveListen, addr)
	}

	srv.SetListener(ln)
	srv.SetAddr(addr)
	printReachable(addr, &opts)
	return srv.Start(cmd.Context())
}

// printReachable 打印对端可连的本机地址清单（多网卡全列）。
// 监听宿主为空/"0.0.0.0"/all 时列出全部非回环 IP；绑定具体网卡则只列该 IP。
func printReachable(addr string, opts *agent.Options) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return // 解析失败则跳过地址提示，不阻塞启动
	}
	fmt.Printf("agent 串口代理已启动:\n")
	host, _, _ := net.SplitHostPort(addr)
	switch host {
	case "", "0.0.0.0", "::", "all":
		for _, ip := range nonLoopbackIPs() {
			fmt.Printf("  地址: http://%s:%s\n", ip, port)
		}
	default:
		fmt.Printf("  地址: http://%s:%s\n", host, port)
	}
	if opts.WriteEnabled {
		fmt.Printf("  写接口已开启，token: %s\n", opts.WriteToken)
	}
}

// nonLoopbackIPs 枚举本机全部非回环 IP（IPv4 优先，IPv6 一并返回）。
func nonLoopbackIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP
		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		out = append(out, ip.String())
	}
	return out
}

// listenAutoIncrement 尝试绑定 baseAddr；若目标端口被占用，依次递增端口号直到成功。
// 返回已绑定的 listener 与（可能递增后的）实际监听地址。baseAddr 需形如 ":7100" 或 "host:7100"。
func listenAutoIncrement(baseAddr string, maxTries ...int) (net.Listener, string, error) {
	const defaultMax = 10
	max := defaultMax
	if len(maxTries) > 0 && maxTries[0] > 0 {
		max = maxTries[0]
	}

	host, portStr, err := net.SplitHostPort(baseAddr)
	if err != nil {
		return nil, "", fmt.Errorf("非法监听地址 %q: %w", baseAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", fmt.Errorf("非法端口 %q: %w", portStr, err)
	}

	var lastErr error
	for i := 0; i <= max; i++ {
		addr := fmt.Sprintf("%s:%d", host, port+i)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, addr, nil
		}
		lastErr = err
	}
	return nil, "", fmt.Errorf("监听失败（已尝试 %d 个端口）: %w", max+1, lastErr)
}

// selectPort 自动选择串口：枚举可用端口并决定如何连接。
// 返回所选端口名。需交互（多端口）时从 stdin 读取用户选择。
func selectPort(drv serial.Driver) (string, error) {
	ports, err := drv.ListPorts()
	if err != nil {
		return "", err
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("未发现可用串口，请用 --port 指定")
	}

	if len(ports) == 1 {
		fmt.Printf("发现唯一串口 %s，自动连接（波特率 %d）\n", ports[0], serveBaud)
		return ports[0], nil
	}

	// 多个串口：列出并让用户选择（回车使用默认第 1 个，0 取消）。
	fmt.Println("发现多个可用串口，请选择要监听的串口：")
	for i, p := range ports {
		fmt.Printf("  [%d] %s\n", i+1, p)
	}
	fmt.Printf("选择（回车默认 %d）：", 1)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("读取选择: %w", err)
	}

	line = strings.TrimSpace(line)
	if line == "" {
		fmt.Printf("使用默认串口 %s\n", ports[0])
		return ports[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(ports) {
		return "", fmt.Errorf("无效选择 %q", line)
	}
	fmt.Printf("使用串口 %s（波特率 %d）\n", ports[n-1], serveBaud)
	return ports[n-1], nil
}

func runAgentProbe(cmd *cobra.Command, args []string) error {
	drv, err := serial.NewDriver()
	if err != nil {
		return err
	}
	_ = cmd

	// 无端口参数：仅枚举可用 COM。
	if len(args) == 0 {
		ports, err := drv.ListPorts()
		if err != nil {
			return err
		}
		if len(ports) == 0 {
			fmt.Println("未发现可用串口")
			return nil
		}
		fmt.Println("可用串口：")
		for _, p := range ports {
			fmt.Printf("  %s\n", p)
		}
		return nil
	}

	port := args[0]
	res, err := agent.Probe(drv, port)
	if err != nil {
		return err
	}
	fmt.Printf("串口 %s 波特率探测结果（按推荐度排序）：\n", port)
	for _, r := range res {
		mark := "   "
		if r.Recommended {
			mark = " * "
		}
		fmt.Printf("%s%6d bps  可打印=%.2f  换行=%d  样本=%q\n",
			mark, r.Baud, r.Printable, r.Newlines, r.Sample)
	}
	fmt.Println("(* 为推荐档位。若数据为可读文本且带换行，即可能为正确波特率。)")
	return nil
}

func init() {
	agentCmd.AddCommand(agentServeCmd, agentProbeCmd)
	rootCmd.AddCommand(agentCmd)

	fs := agentServeCmd.Flags()
	fs.StringVar(&serveListen, "listen", ":7100", "HTTP 监听地址")
	fs.StringVar(&servePort, "port", "", "串口名，如 COM5")
	fs.IntVar(&serveBaud, "baud", 115200, "波特率")
	fs.BoolVar(&serveEnableWrite, "enable-write", false, "开启下行写接口 /api/write（默认关；未指定 --token 时每次生成随机 token 并控制台输出）")
	fs.StringVar(&serveToken, "token", "", "固定写 token（配合 --enable-write；不设则每次随生成随机值，不落文件）")
	fs.BoolVar(&serveNoLocalLog, "no-local-log", false, "关闭本地默认落盘（默认开启）")
	fs.StringVar(&serveOutDir, "out-dir", "logs", "本地落盘日志输出目录，默认运行目录下 logs/")
}
