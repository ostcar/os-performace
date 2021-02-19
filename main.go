package main

// reading_client simulates clients, that login to openslides and after a
// successfull login send all request, that the client usual sends.
import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
)

const (
	pathLogin      = "/apps/users/login/"
	pathWhoami     = "/apps/users/whoami/"
	pathServertime = "/apps/core/servertime/"
	pathConstants  = "/apps/core/constants/"
)

func main() {
	if err := run(os.Args); err != nil {
		log.Printf("Error: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := loadConfig(args[1:])
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	switch cfg.testCase {
	case testBrowser:
		err = runBrowser(cfg)
	case testConnect:
		err = runKeepOpen(cfg)
	default:
		err = fmt.Errorf("unknown testCase")
	}

	if err != nil {
		return fmt.Errorf("running test: %w", err)
	}
	return nil
}

func runBrowser(cfg *Config) error {
	log.Printf("using %d clients to %s", cfg.clientCount, cfg.domain)

	bar := pb.StartNew(cfg.clientCount * 5)

	clients := make([]*client, cfg.clientCount)
	for i := range clients {
		c, err := newClient(cfg.domain, cfg.username, cfg.passwort, bar)
		if err != nil {
			return fmt.Errorf("creating client: %w", err)
		}
		clients[i] = c
	}

	start := time.Now()

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *client) {
			defer wg.Done()
			if err := c.browser(); err != nil {
				log.Printf("Client failed: %v", err)
			}
		}(c)
	}

	wg.Wait()
	bar.Finish()
	log.Printf("Run for %v", time.Now().Sub(start))
	return nil
}

func runKeepOpen(cfg *Config) error {
	path := "/system/autoupdate"

	c, err := newClient(cfg.domain, cfg.username, cfg.passwort, nil)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	if err := c.login(); err != nil {
		return fmt.Errorf("login client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < cfg.clientCount; i++ {
		wg.Add(1)
		go func(first bool) {
			defer wg.Done()

			r, err := c.keepOpen(ctx, path)
			if err != nil {
				log.Println("Can not create connection: %w", err)
				return
			}
			defer r.Close()

			if first {
				// TODO: Listen to ctx.Done
				scanner := bufio.NewScanner(r)
				scanner.Buffer(make([]byte, 10), 1000000)
				for scanner.Scan() {
					text := scanner.Text()
					if len(text) > 50 {
						text = text[:50] + fmt.Sprintf("... [%d bytes]", len(text))
					}
					log.Println(text)
				}
				if err := scanner.Err(); err != nil {
					log.Println("Can not read body: %w", err)
					return
				}
			} else {
				readToNothing(ctx, r)
			}

		}(i == 0)
	}

	wg.Wait()
	return nil
}

func readToNothing(ctx context.Context, r io.Reader) {
	go func() {
		var err error
		buf := make([]byte, 1000)
		for err == nil && ctx.Err() == nil {
			_, err = r.Read(buf)
		}
	}()

	<-ctx.Done()
	return
}

type client struct {
	domain             string
	hc                 *http.Client
	username, password string
	bar                *pb.ProgressBar
}

// newClient creates a client object. No requests are sent.
func newClient(domain, username, password string, bar *pb.ProgressBar) (*client, error) {
	c := &client{
		domain:   "https://" + domain,
		username: username,
		password: password,
		bar:      bar,
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}
	c.hc = &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	return c, nil
}

// browser sends the request each browser tab sends.
func (c *client) browser() error {
	if err := c.login(); err != nil {
		return fmt.Errorf("login client: %w", err)
	}

	if c.bar != nil {
		c.bar.Increment()
	}

	var wg sync.WaitGroup

	for _, path := range []string{
		pathWhoami,
		pathLogin,
		pathServertime,
		pathConstants,
	} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()

			if c.bar != nil {
				defer c.bar.Increment()
			}

			if err := c.get(context.Background(), path); err != nil {
				log.Printf("Error get request to %s: %v", path, err)
			}
		}(path)
	}

	wg.Wait()
	return nil
}

func (c *client) keepOpen(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.domain+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := checkStatus(c.hc.Do(req))
	if err != nil {
		return nil, fmt.Errorf("sending %s request: %w", path, err)
	}
	return resp.Body, nil
}

// login uses the username and password to login the client. Sets the returned
// cookie for later requests.
func (c *client) login() error {
	url := c.domain + pathLogin
	payload := fmt.Sprintf(`{"username": "%s", "password": "%s"}`, c.username, c.password)

	resp, err := checkStatus(c.hc.Post(url, "application/json", strings.NewReader(payload)))
	if err != nil {
		return fmt.Errorf("sending login request: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (c *client) get(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.domain+path, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)

	}

	resp, err := checkStatus(c.hc.Do(req))
	if err != nil {
		return fmt.Errorf("sending %s request: %w", path, err)
	}
	resp.Body.Close()
	return nil
}

func checkStatus(resp *http.Response, err error) (*http.Response, error) {
	if err != nil {
		return nil, fmt.Errorf("sending login request: %w", err)
	}

	if resp.StatusCode != 200 {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			body = []byte("[can not read body]")
		}
		resp.Body.Close()
		return nil, fmt.Errorf("got status %s: %s", resp.Status, body)
	}
	return resp, nil
}

// Config contains all settings that can be changed with command line options.
type Config struct {
	testCase    int
	clientCount int
	domain      string
	username    string
	passwort    string
}

func loadConfig(args []string) (*Config, error) {
	cfg := new(Config)
	f := flag.NewFlagSet("args", flag.ExitOnError)

	f.IntVar(&cfg.clientCount, "n", 10, "number of connections to use")
	f.StringVar(&cfg.domain, "d", "localhost:8000", "host and port of the server to test")
	f.StringVar(&cfg.username, "u", "admin", "username to use for login")
	f.StringVar(&cfg.passwort, "p", "admin", "password to use for login")

	test := f.String("t", "", "testcase [browser,connect]")

	if err := f.Parse(args); err != nil {
		return nil, fmt.Errorf("parsing flags: %w", err)
	}

	if len(flag.Args()) > 0 {
		return nil, fmt.Errorf("invalid arguments: %s", strings.Join(flag.Args(), " "))
	}

	switch *test {
	case "browser":
		cfg.testCase = testBrowser
	case "connect":
		cfg.testCase = testConnect
	default:
		return nil, fmt.Errorf("invalid testcase %s", *test)
	}

	return cfg, nil
}

const (
	testBrowser = iota
	testConnect = iota
)
