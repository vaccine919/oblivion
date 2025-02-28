package tun2socks

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"tun2socks/lwip"

	"github.com/bepass-org/wireguard-go/app"
	L "github.com/xjasonlyu/tun2socks/v2/log"
)

// Variables to hold flag values.
var (
	verbose        *bool
	bindAddress    *string
	endpoint       *string
	license        *string
	country        *string
	psiphonEnabled *bool
	gool           *bool
	scan           *bool
	rtt            *int
	logMessages    []string
	mu             sync.Mutex
	wg             sync.WaitGroup
	cancelFunc     context.CancelFunc
)

type logWriter struct{}

func (writer logWriter) Write(bytes []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	logMessages = append(logMessages, string(bytes))
	return len(bytes), nil
}

func parseCommandLine(argStr string) ([]string, error) {
	// Regular expression to match flags (like -b or --gool) and their optional values
	re := regexp.MustCompile(`(--?\w+)([= ]("[^"]*"|'[^']*'|[^ ]+))?`)
	matches := re.FindAllStringSubmatch(argStr, -1)

	var args []string
	for _, match := range matches {
		args = append(args, match[1]) // Flag name
		if match[3] != "" {
			// Remove surrounding quotes if present
			value := strings.Trim(match[3], `"'`)
			args = append(args, value) // Flag value
		}
	}
	return args, nil
}

func RunWarp(argStr, path string, fd int) {
	logger := logWriter{}
	log.SetOutput(logger)
	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w

	L.SetLevel(L.DebugLevel)
	L.SetOutput(logger)

	go func(reader io.Reader) {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			logger.Write([]byte(scanner.Text()))
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintln(os.Stderr, "There was an error with the scanner", err)
		}
	}(r)
	if err := os.Chdir(path); err != nil {
		log.Fatal("Error changing to 'main' directory:", err)
	}
	// Parse command-line arguments.
	args, err := parseCommandLine(argStr)
	if err != nil {
		log.Fatal(err)
	}
	fs := flag.NewFlagSet("tun2socks", flag.ExitOnError)
	verbose = fs.Bool("v", false, "verbose")
	bindAddress = fs.String("b", "127.0.0.1:8086", "socks bind address")
	endpoint = fs.String("e", "notset", "warp clean ip")
	license = fs.String("k", "notset", "license key")
	country = fs.String("country", "", "psiphon country code in ISO 3166-1 alpha-2 format")
	psiphonEnabled = fs.Bool("cfon", false, "enable psiphonEnabled over warp")
	gool = fs.Bool("gool", false, "enable warp gooling")
	scan = fs.Bool("scan", false, "enable warp scanner(experimental)")
	rtt = flag.Int("rtt", 1000, "scanner rtt threshold, default 1000")

	err = fs.Parse(args)
	if err != nil {
		log.Fatalf("Failed to parse flags: %v", err)
	}

	// Setup context with cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel
	wg.Add(1)

	// Start your long-running process.
	go runServer(ctx, fd)

	// Wait for interrupt signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		// Received an interrupt signal, shut down.
		log.Println("Shutting down server...")
		cancelFunc()
	case <-ctx.Done():
		// Context was cancelled, perhaps from another part of the app calling Shutdown().
	}

	// Wait for the server goroutine to finish.
	wg.Wait()
	log.Println("Server shut down gracefully.")
}

func runServer(ctx context.Context, fd int) {
	// Ensuring a cleanup operation even in the case of an error
	defer func() {
		// Perform cleanup and exit.
		lwip.Stop()
		log.Println("Cleanup done, exiting runServer goroutine.")

		defer wg.Done()
	}()

	// Start wireguard-go and gvisor-tun2socks.
	go func() {
		err := app.RunWarp(*psiphonEnabled, *gool, *scan, *verbose, *country, *bindAddress, *endpoint, *license, ctx, *rtt)
		if err != nil {
			log.Println(err)
		}
	}()

	tun2socksStartOptions := &lwip.Tun2socksStartOptions{
		TunFd:        fd,
		Socks5Server: strings.Replace(*bindAddress, "0.0.0.0", "127.0.0.1", -1),
		FakeIPRange:  "24.0.0.0/8",
		MTU:          0,
		EnableIPv6:   true,
		AllowLan:     true,
	}
	lwip.Start(tun2socksStartOptions)

	// Wait for context cancellation.
	<-ctx.Done()
}

// Shutdown can be called to stop the server from another part of the app.
func Shutdown() {
	if cancelFunc != nil {
		cancelFunc()
		os.Exit(0)
	}
}

func GetLogMessages() string {
	mu.Lock()
	defer mu.Unlock()
	if len(logMessages) == 0 {
		return ""
	}
	logs := strings.Join(logMessages, "\n")
	logMessages = nil // Clear logMessages for better memory management
	return logs
}
