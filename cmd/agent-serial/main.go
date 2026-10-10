// Command agent-serial is an MCP stdio server that lets coding agents talk
// to embedded devices over serial consoles.
//
// Device output is buffered continuously between tool calls, reads support
// capture windows with idle and byte cutoffs, and the expect tool reacts to
// device output with pattern-triggered sends.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wltbagent/agent-serial/internal/serial"
	"github.com/wltbagent/agent-serial/internal/server"
)

const version = "0.1.0"

func main() {
	var (
		allowPorts = flag.String("allow-ports", "", "comma-separated substrings; only serial device paths containing one may be opened (default: all allowed)")
		blockPorts = flag.String("block-ports", "", "comma-separated substrings; serial device paths containing one are refused")
		allowHosts = flag.String("allow-hosts", "", "comma-separated substrings; only telnet/ssh targets containing one may be opened (default: all allowed)")
		blockHosts = flag.String("block-hosts", "", "comma-separated substrings; telnet/ssh targets containing one are refused")
		maxConns   = flag.Int("max-connections", serial.DefaultMaxConnections, "maximum simultaneous open connections")
		ringSize   = flag.Int("ring-size", serial.DefaultRingCapacity, "per-connection receive buffer in bytes")
		knownHosts = flag.String("known-hosts", "", "path to the SSH known-hosts file for first-use pinning (default: <user config dir>/agent-serial/known_hosts)")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	reg := serial.NewRegistry(serial.RegistryOptions{
		MaxConnections:    *maxConns,
		RingCapacity:      *ringSize,
		AllowedPorts:      splitList(*allowPorts),
		BlockedPorts:      splitList(*blockPorts),
		AllowedHosts:      splitList(*allowHosts),
		BlockedHosts:      splitList(*blockHosts),
		SSHKnownHostsFile: *knownHosts,
	})
	defer reg.CloseAll()

	srv := server.New(reg, version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	slog.Info("agent-serial listening on stdio", "version", version)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "agent-serial:", err)
		os.Exit(1)
	}
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
