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

// sendConfig aggregates the send subcommand inputs and decides between remote and local writes.
type sendConfig struct {
	URL   string // non-empty → remote write: POST /api/write to the agent
	Port  string // non-empty and URL empty → local direct write to a local serial port
	Baud  int
	Cmd   string
	Token string // auth token for remote writes
}

// sendWriter abstracts the send function: remote and local implementations share
// the same interface (also reused by a future WebUI command window).
type sendWriter interface {
	Send(ctx context.Context, data []byte) error
}

// remoteWriter sends commands remotely via the agent's POST /api/write.
type remoteWriter struct {
	base  string
	token string
	cli   *http.Client
}

// Send builds a POST form data=<content> with a Bearer token (if set); any non-2xx status is treated as failure.
func (w *remoteWriter) Send(ctx context.Context, data []byte) error {
	form := url.Values{}
	form.Set("data", string(data))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/api/write", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}
	resp, err := w.cli.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent returned %s", resp.Status)
	}
	return nil
}

// localWriter writes directly through the local serial driver (no auth; the local machine is the identity).
type localWriter struct {
	drv  serial.Driver
	port string
	baud int
}

// Send opens the port, writes the data, then closes it immediately.
func (w *localWriter) Send(ctx context.Context, data []byte) error {
	p, err := w.drv.Open(w.port, w.baud)
	if err != nil {
		return fmt.Errorf("opening serial port %s: %w", w.port, err)
	}
	defer p.Close()
	n, err := p.Write(data)
	if err != nil {
		return fmt.Errorf("writing to serial port: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("incomplete serial write: %d/%d bytes", n, len(data))
	}
	return nil
}

// escapeCmd replaces the literal escapes \r \n \t in the command with real control
// characters (compatible with manual escaping).
func escapeCmd(s string) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t").Replace(s)
}

// sendCmd is the serial write subcommand, separate from run (log reading).
var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a command to the serial port",
	Long: "Send a command to the serial port (defaults to the local run --enable-write write channel).\n" +
			"  Examples:\n" +
			"    serialcat send \"ls -al\"                        # command follows directly; spaces join into a single command string\n" +
			"    serialcat send \"AT+OPEN\\r\\n\"                  # supports \\r \\n \\t escapes\n" +
			"    serialcat send --cmd \"echo hi\" arg1 arg2        # --cmd and positional args can be combined; result is \"echo hi arg1 arg2\"\n" +
			"    serialcat send --port COM5 \"ls\"                # raw write: bypass run/agent and write directly to the local serial port (blind, no echo)\n" +
		"  Notes: a trailing carriage return (\\r) is appended by default to trigger the device; use --raw for exact bytes. Failures return an error and a non-zero exit code.",
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
	// Command source: --cmd wins; otherwise positional args are joined (send the concrete command directly).
	cmd := sendCmdS
	if cmd == "" {
		cmd = strings.Join(args, " ")
	}
	if cmd == "" {
		return fmt.Errorf("missing command to send (use --cmd or pass it directly as an argument)")
	}
	cfg := sendConfig{URL: sendURL, Port: sendPort, Baud: sendBaud, Cmd: cmd, Token: sendToken}

	var w sendWriter
	if cfg.Port != "" {
		// Local direct write (blind): only an explicit --port uses the local serial port, bypassing the run/agent holder.
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
		// Over HTTP: defaults to the local run --enable-write write channel (127.0.0.1:7100).
		// --url can also point to a remote agent. Accepts URLs with or without http(s)://:
		// http:// is prepended when the scheme is missing.
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
	// By default a single carriage return (\r) is appended to trigger the device, like pressing Enter in SecureCRT;
	// the ICRNL of a Linux serial shell turns CR into NL, triggering once cleanly without blank lines.
	// If the data already ends with \r or \n, nothing extra is appended; --raw disables the auto-append and sends exactly as given.
	if !sendRawNoNL && len(data) > 0 && data[len(data)-1] != '\n' && data[len(data)-1] != '\r' {
		data = append(data, '\r')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.Send(ctx, data); err != nil {
		return err
	}
	fmt.Printf("command sent: %q\n", cfg.Cmd)
	return nil
}

func init() {
	rootCmd.AddCommand(sendCmd)
	fs := sendCmd.Flags()
	fs.StringVar(&sendURL, "url", "127.0.0.1:7100", "write channel address (default: local run --enable-write), e.g. http://192.168.1.10:7100")
	fs.StringVar(&sendPort, "port", "", "local serial port name, e.g. COM5 (local write, Windows only)")
	fs.IntVar(&sendBaud, "baud", 115200, "baud rate for local writes")
	fs.StringVar(&sendCmdS, "cmd", "", "command to send (supports \\r \\n \\t escapes)")
	fs.StringVar(&sendToken, "token", "", "auth token for remote writes (used with --url; the token generated by --enable-write)")
	fs.BoolVar(&sendRawNoNL, "raw", false, "send exactly as given, without appending a carriage return (\\r)")
}
