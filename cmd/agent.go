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

// agentCmd is the Windows serial agent subcommand (runs on a Windows machine with serial ports).
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Serial agent (currently Windows-only; support on other systems TBD)",
	Args:  cobra.NoArgs,
}

// agentServeCmd starts the serial HTTP agent service.
var agentServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the serial agent service",
	Long: "Run this on the machine with the serial port attached: it auto-connects the port (auto-selected when not specified) " +
		"and serves the serial data over the network for serialcat on other machines.\n" +
		"After startup it prints the addresses reachable from this machine; combine with --enable-write to send commands from the peer.\n" +
		"Arguments: --listen HTTP listen address (default :7100); --port serial port name (e.g. COM5, auto-selected if empty); " +
		"--baud baud rate (default 115200); --enable-write enable downstream writes; --token fixed write token (random if empty); " +
		"--no-local-log disable local log files; --out-dir local log output directory (default logs/ under the working directory).",
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

// agentProbeCmd probes serial port availability and baud rates.
var agentProbeCmd = &cobra.Command{
	Use:   "probe [port]",
	Short: "List or probe serial ports",
	Long: "Without arguments, lists all local serial ports; with one port name, tests it and suggests the most recommended baud rate.\n" +
		"Arguments: <port> serial port name (e.g. COM5), optional.",
	Args: cobra.MaximumNArgs(1),
	RunE: runAgentProbe,
}

func runAgentServe(cmd *cobra.Command, _ []string) error {
	drv, err := serial.NewDriver()
	if err != nil {
		return err
	}

	// When no port is specified, auto-detect and select one:
	//   no port → error; only one → connect directly; multiple → interactive console selection.
	port := servePort
	if port == "" {
		chosen, err := selectPort(drv)
		if err != nil {
			return err
		}
		port = chosen
	}

	// --enable-write: the token comes from --token (fixed) or a fresh random value
	// generated per run (printed to the console, never written to a file).
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

	// Listen port: if occupied, auto-increment by 1 until bind succeeds or the max tries are exceeded.
	ln, addr, err := listenAutoIncrement(serveListen)
	if err != nil {
		return err
	}
	defer ln.Close()
	if addr != serveListen {
		fmt.Printf("port %s is occupied, using %s instead\n", serveListen, addr)
	}

	srv.SetListener(ln)
	srv.SetAddr(addr)
	printReachable(addr, &opts)
	return srv.Start(cmd.Context())
}

// printReachable prints the local addresses reachable by the peer (all NICs listed).
// When the listen host is empty/"0.0.0.0"/all, lists every non-loopback IP; when bound to a specific NIC, lists only that IP.
func printReachable(addr string, opts *agent.Options) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return // skip the address hint on parse failure; do not block startup
	}
	fmt.Printf("serial agent started:\n")
	host, _, _ := net.SplitHostPort(addr)
	switch host {
	case "", "0.0.0.0", "::", "all":
		for _, ip := range nonLoopbackIPs() {
			fmt.Printf("  address: http://%s:%s\n", ip, port)
		}
	default:
		fmt.Printf("  address: http://%s:%s\n", host, port)
	}
	if opts.WriteEnabled {
		fmt.Printf("  write endpoint enabled, token: %s\n", opts.WriteToken)
	}
}

// nonLoopbackIPs enumerates all local non-loopback IPs (IPv4 first; IPv6 also returned).
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

// listenAutoIncrement tries to bind baseAddr; if the target port is occupied, it
// increments the port number until success. It returns the bound listener and the
// actual listen address (possibly incremented). baseAddr must look like ":7100" or "host:7100".
func listenAutoIncrement(baseAddr string, maxTries ...int) (net.Listener, string, error) {
	const defaultMax = 10
	max := defaultMax
	if len(maxTries) > 0 && maxTries[0] > 0 {
		max = maxTries[0]
	}

	host, portStr, err := net.SplitHostPort(baseAddr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid listen address %q: %w", baseAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid port %q: %w", portStr, err)
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
	return nil, "", fmt.Errorf("listen failed (tried %d ports): %w", max+1, lastErr)
}

// selectPort auto-selects a serial port: enumerates available ports and decides how to connect.
// It returns the chosen port name. When interaction is needed (multiple ports), it reads the user's choice from stdin.
func selectPort(drv serial.Driver) (string, error) {
	ports, err := drv.ListPorts()
	if err != nil {
		return "", err
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("no serial port found, specify one with --port")
	}

	if len(ports) == 1 {
		fmt.Printf("found a single serial port %s, connecting automatically (baud %d)\n", ports[0], serveBaud)
		return ports[0], nil
	}

	// Multiple ports: list them and let the user choose (Enter picks the default first one, 0 cancels).
	fmt.Println("found multiple serial ports, choose which one to listen on:")
	for i, p := range ports {
		fmt.Printf("  [%d] %s\n", i+1, p)
	}
	fmt.Printf("choice (Enter defaults to %d): ", 1)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading selection: %w", err)
	}

	line = strings.TrimSpace(line)
	if line == "" {
		fmt.Printf("using default serial port %s\n", ports[0])
		return ports[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(ports) {
		return "", fmt.Errorf("invalid selection %q", line)
	}
	fmt.Printf("using serial port %s (baud %d)\n", ports[n-1], serveBaud)
	return ports[n-1], nil
}

func runAgentProbe(cmd *cobra.Command, args []string) error {
	drv, err := serial.NewDriver()
	if err != nil {
		return err
	}
	_ = cmd

	// No port argument: only enumerate available COM ports.
	if len(args) == 0 {
		ports, err := drv.ListPorts()
		if err != nil {
			return err
		}
		if len(ports) == 0 {
			fmt.Println("no serial port found")
			return nil
		}
		fmt.Println("available serial ports:")
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
	fmt.Printf("baud rate probe results for port %s (sorted by recommendation):\n", port)
	for _, r := range res {
		mark := "   "
		if r.Recommended {
			mark = " * "
		}
		fmt.Printf("%s%6d bps  printable=%.2f  newlines=%d  sample=%q\n",
			mark, r.Baud, r.Printable, r.Newlines, r.Sample)
	}
	fmt.Println("(* marks the recommended rate. If the data is readable text with newlines, the baud rate is likely correct.)")
	return nil
}

func init() {
	agentCmd.AddCommand(agentServeCmd, agentProbeCmd)
	rootCmd.AddCommand(agentCmd)

	fs := agentServeCmd.Flags()
	fs.StringVar(&serveListen, "listen", ":7100", "HTTP listen address")
	fs.StringVar(&servePort, "port", "", "serial port name, e.g. COM5")
	fs.IntVar(&serveBaud, "baud", 115200, "baud rate")
	fs.BoolVar(&serveEnableWrite, "enable-write", false, "enable the downstream write endpoint /api/write (default off; a random token is generated and printed to the console when --token is not set)")
	fs.StringVar(&serveToken, "token", "", "fixed write token (used with --enable-write; a random one is generated per run when not set, never written to a file)")
	fs.BoolVar(&serveNoLocalLog, "no-local-log", false, "disable local log files (enabled by default)")
	fs.StringVar(&serveOutDir, "out-dir", "logs", "local log output directory, default logs/ under the working directory")
}
