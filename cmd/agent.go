package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"logdiag/internal/agent"
)

// agentCmd 是 Windows 串口代理子命令（在带串口的 Windows 机器上运行）。
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Windows 串口代理（在带串口的 Windows 机器上运行）",
	Args:  cobra.NoArgs,
}

// agentServeCmd 启动串口 HTTP 代理服务。
var agentServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "启动串口 HTTP 代理服务",
	Long:  "打开串口并暴露 /api/ports /api/stream /api/write /api/probe 接口，供远端 logdiag 拉流。",
	Args:  cobra.NoArgs,
	RunE:  runAgentServe,
}

var (
	serveListen string
	servePort   string
	serveBaud   int
)

// agentProbeCmd 探测串口可用性与波特率。
var agentProbeCmd = &cobra.Command{
	Use:   "probe [port]",
	Short: "探测串口可用性并推荐波特率",
	Long:  "不带参数时枚举所有 COM；带端口时对常见波特率采样打分，推荐最可能正确的档位。",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAgentProbe,
}

func runAgentServe(cmd *cobra.Command, _ []string) error {
	drv, err := agent.NewDriver()
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

	srv, err := agent.NewServer(serveListen, port, serveBaud, drv)
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
	return srv.Start(cmd.Context())
}

// listenAutoIncrement 尝试绑定 baseAddr；若目标端口被占用，依次递增端口号直到成功。
// 返回已绑定的 listener 与（可能递增后的）实际监听地址。baseAddr 需形如 ":9100" 或 "host:9100"。
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
func selectPort(drv agent.Driver) (string, error) {
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
	drv, err := agent.NewDriver()
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
	fs.StringVar(&serveListen, "listen", ":9100", "HTTP 监听地址")
	fs.StringVar(&servePort, "port", "", "串口名，如 COM5")
	fs.IntVar(&serveBaud, "baud", 115200, "波特率")
}
