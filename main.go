package main

// reading_client simulates clients, that login to openslides and after a
// successfull login send all request, that the client usual sends.
import (
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"

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
	cfg, err := loadConfig(args)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	log.Printf("using %d clients to %s", cfg.clientCount, cfg.domain)

	clients := make([]*client, cfg.clientCount)
	for i := range clients {
		c, err := newClient(cfg.domain, cfg.username, cfg.passwort)
		if err != nil {
			return fmt.Errorf("creating client: %w", err)
		}
		clients[i] = c
	}

	bar := pb.StartNew(cfg.clientCount)
	defer bar.Finish()

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *client) {
			defer wg.Done()
			defer bar.Increment()
			if err := c.run(); err != nil {
				log.Printf("Client failed: %v", err)
			}
		}(c)
	}

	wg.Wait()
	return nil
}

type client struct {
	domain             string
	hc                 *http.Client
	username, password string
}

// newClient creates a client object. No requests are sent.
func newClient(domain, username, password string) (*client, error) {
	c := &client{
		domain:   "https://" + domain,
		username: username,
		password: password,
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

// run sends the normal requests
func (c *client) run() error {
	if err := c.login(); err != nil {
		return fmt.Errorf("login client: %w", err)
	}

	for _, path := range []string{
		pathWhoami,
		pathLogin,
		pathServertime,
		pathConstants,
	} {
		if err := c.get(path); err != nil {
			return fmt.Errorf("get request to %s: %w", path, err)
		}
	}
	return nil
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

func (c *client) get(path string) error {
	resp, err := checkStatus(c.hc.Get(c.domain + path))
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
	clientCount int
	domain      string
	username    string
	passwort    string
}

func loadConfig(args []string) (*Config, error) {
	var cfg Config
	f := flag.NewFlagSet("args", flag.ExitOnError)

	f.IntVar(&cfg.clientCount, "n", 10, "number of connections to use")
	f.StringVar(&cfg.domain, "d", "localhost:8000", "host and port of the server to test")
	f.StringVar(&cfg.username, "u", "admin", "username to use for login")
	f.StringVar(&cfg.passwort, "p", "admin", "password to use for login")

	if err := f.Parse(args[1:]); err != nil {
		return nil, fmt.Errorf("parsing flags: %w", err)
	}

	if len(flag.Args()) > 0 {
		return nil, fmt.Errorf("invalid arguments: %s", strings.Join(flag.Args(), " "))
	}

	return &cfg, nil
}
