package config

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL, Address, Origin, SetupToken, WebDir            string
	AllowedCIDRs, AllowedHosts, DemoURL                         string
	NotificationCIDRs, NotificationHosts, NotificationHTTPHosts string
	Key                                                         []byte
	SecureCookies                                               bool
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Address: env("SHERLOCK_ADDR", "127.0.0.1:8080"), Origin: env("SHERLOCK_ORIGIN", "http://localhost:8080"), SetupToken: os.Getenv("SHERLOCK_SETUP_TOKEN"), WebDir: env("SHERLOCK_WEB_DIR", "web/dist")}
	c.AllowedCIDRs = os.Getenv("SHERLOCK_ALLOWED_CIDRS")
	c.AllowedHosts = os.Getenv("SHERLOCK_ALLOWED_HOSTS")
	c.DemoURL = os.Getenv("SHERLOCK_DEMO_URL")
	c.NotificationCIDRs = os.Getenv("SHERLOCK_NOTIFICATION_CIDRS")
	c.NotificationHosts = os.Getenv("SHERLOCK_NOTIFICATION_HOSTS")
	c.NotificationHTTPHosts = os.Getenv("SHERLOCK_NOTIFICATION_HTTP_HOSTS")
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	var err error
	c.Key, err = base64.StdEncoding.DecodeString(os.Getenv("SHERLOCK_ENCRYPTION_KEY"))
	if err != nil || len(c.Key) != 32 {
		return c, errors.New("SHERLOCK_ENCRYPTION_KEY must be base64 encoding of 32 bytes; run make setup")
	}
	if len(c.SetupToken) < 32 {
		return c, errors.New("SHERLOCK_SETUP_TOKEN must contain at least 32 characters; run make setup")
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return c, errors.New("SHERLOCK_ORIGIN must be a bare http(s) origin without path")
	}
	c.SecureCookies = u.Scheme == "https"
	ip := net.ParseIP(u.Hostname())
	if !c.SecureCookies && !(u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return c, errors.New("remote origins require HTTPS")
	}
	return c, nil
}
func env(k, fallback string) string {
	if s := strings.TrimSpace(os.Getenv(k)); s != "" {
		return s
	}
	return fallback
}
