package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JFRabbit/serialcat/pkg/serial"
)

// sendConfig 汇聚 send 子命令的入参，决定走远程写还是本地写。
type sendConfig struct {
	URL   string // 非空 → 远程写成对 agent POST /api/write
	Port  string // 非空且 URL 为空 → 本地直连本机串口
	Baud  int
	Cmd   string
	Token string // 远程写鉴权令牌
}

// sendWriter 是发送函数抽象：远程/本地两种实现，二期 WebUI 命令窗口复用同一接口。
type sendWriter interface {
	Send(ctx context.Context, data []byte) error
}

// remoteWriter 通过 agent 的 POST /api/write 远程下发。
type remoteWriter struct {
	base  string
	token string
	cli   *http.Client
}

// Send 构造 POST 表单 data=<内容>，携带 Bearer token（若设置）；以非 2xx 视为失败。
func (w *remoteWriter) Send(ctx context.Context, data []byte) error {
	form := url.Values{}
	form.Set("data", string(data))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/api/write", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}
	resp, err := w.cli.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent 返回 %s", resp.Status)
	}
	return nil
}

// localWriter 通过本机串口驱动直接下发（不鉴权，本机即身份）。
type localWriter struct {
	drv  serial.Driver
	port string
	baud int
}

// Send 打开串口写入数据后立即关闭。
func (w *localWriter) Send(ctx context.Context, data []byte) error {
	p, err := w.drv.Open(w.port, w.baud)
	if err != nil {
		return fmt.Errorf("打开串口 %s: %w", w.port, err)
	}
	defer p.Close()
	n, err := p.Write(data)
	if err != nil {
		return fmt.Errorf("写入串口: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("串口写入不完整: %d/%d 字节", n, len(data))
	}
	return nil
}

// escapeCmd 将命令中的 \r \n \t 字面转义统一替换为真实控制字符（兼容用户手工转义）
func escapeCmd(s string) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t").Replace(s)
}

// sendCmd 是串口下行命令子命令，与 run（读日志）分离。
var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "向串口下发一条命令",
	Long: "向串口下发一条命令（默认发本机 run --enable-write 写通道）。\n" +
			"  示例：\n" +
			"    serialcat send \"ls -al\"                        # 直接跟命令，空格自动拼成一个命令串\n" +
			"    serialcat send \"AT+OPEN\\r\\n\"                  # 支持 \\r \\n \\t 转义\n" +
			"    serialcat send --cmd \"echo hi\" arg1 arg2        # --cmd 与位置参数可同时用,结果为 \"echo hi arg1 arg2\"\n" +
			"    serialcat send --port COM5 \"ls\"                # 裸发:不经 run/agent,直接写本机串口(盲发,无回显)\n" +
		"  说明：默认自动补一个回车(\\r)触发设备执行；需精确字节时用 --raw。失败即报错并置非零退出码。",
	Args: cobra.ArbitraryArgs,
	RunE: runSend,
}

var (
	sendURL     string
	sendPort    string
	sendBaud    int
	sendCmdS    string
	sendToken   string
	sendRawNoNL bool
)

func runSend(_ *cobra.Command, args []string) error {
	// 命令来源：--cmd 优先；否则用位置参数拼接(直接 send 具体命令)。
	cmd := sendCmdS
	if cmd == "" {
		cmd = strings.Join(args, " ")
	}
	if cmd == "" {
		return fmt.Errorf("缺少要下发的命令（用 --cmd 或直接跟在命令后）")
	}
	cfg := sendConfig{URL: sendURL, Port: sendPort, Baud: sendBaud, Cmd: cmd, Token: sendToken}

	var w sendWriter
	if cfg.Port != "" {
		// 本地直连（盲发）：显式 --port 才走本机串口，绕开 run/agent 持有者。
		drv, err := serial.NewDriver()
		if err != nil {
			return err
		}
		baud := cfg.Baud
		if baud <= 0 {
			baud = 115200
		}
		w = &localWriter{drv: drv, port: cfg.Port, baud: baud}
	} else {
		// 经 HTTP 下发：默认发本机 run --enable-write 的写通道(127.0.0.1:7100)。
		// 也可 --url 指向远程 agent。兼容带/不带 http(s):// 写法:缺协议时默认补 http://。
		base := strings.TrimSpace(cfg.URL)
		base = strings.TrimRight(base, "/")
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}
		w = &remoteWriter{
			base:  base,
			token: cfg.Token,
			cli:   &http.Client{Timeout: 5 * time.Second},
		}
	}

	data := []byte(escapeCmd(cfg.Cmd))
	// 默认自动补单个回车(\r)以触发设备执行,等同 SecureCRT 按 Enter;
	// Linux 串口 shell 的 ICRNL 会把 CR 转成 NL,干净触发一次,不产生空行。
	// 末尾已带 \r 或 \n 则不重复补;--raw 关闭自动补全,按原样精确发送。
	if !sendRawNoNL && len(data) > 0 && data[len(data)-1] != '\n' && data[len(data)-1] != '\r' {
		data = append(data, '\r')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.Send(ctx, data); err != nil {
		return err
	}
	fmt.Printf("命令已下发: %q\n", cfg.Cmd)
	return nil
}

func init() {
	rootCmd.AddCommand(sendCmd)
	fs := sendCmd.Flags()
	fs.StringVar(&sendURL, "url", "127.0.0.1:7100", "写通道地址（默认本机 run --enable-write），如 http://192.168.1.10:7100")
	fs.StringVar(&sendPort, "port", "", "本机串口名，如 COM5（本地写，仅 Windows）")
	fs.IntVar(&sendBaud, "baud", 115200, "本地写波特率")
	fs.StringVar(&sendCmdS, "cmd", "", "要下发的命令（支持 \\r \\n \\t 转义）")
	fs.StringVar(&sendToken, "token", "", "远程写鉴权令牌（配合 --url，--enable-write 生成的 token）")
	fs.BoolVar(&sendRawNoNL, "raw", false, "按原样发送,不自动补回车(\\r)")
}
