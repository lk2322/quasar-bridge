// Command spotify-login signs a Spotify account in for Quasar Bridge (PKCE, no
// client secret) and writes spotify-token.json. Run it on a computer with a
// browser, then copy the file next to config.json on the speaker.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"quasarbridge/internal/spotify"
)

func main() {
	clientID := flag.String("client-id", "", "Client ID of your app on developer.spotify.com")
	out := flag.String("out", "spotify-token.json", "token file to write")
	port := flag.Int("port", 5589, "port of the Redirect URI http://127.0.0.1:<port>/callback")
	noBrowser := flag.Bool("no-browser", false, "only print the sign-in link")
	flag.Parse()
	if *clientID == "" {
		fmt.Fprintln(os.Stderr, "usage: spotify-login -client-id <Client ID> [-out spotify-token.json] [-port 5589]")
		os.Exit(2)
	}
	if err := run(*clientID, *out, *port, !*noBrowser); err != nil {
		fmt.Fprintln(os.Stderr, "spotify-login:", err)
		os.Exit(1)
	}
}

func run(clientID, out string, port int, browser bool) error {
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	verifier := random(48)
	sum := sha256.Sum256([]byte(verifier))
	state := random(16)
	link := spotify.DefaultAccountsURL + "/authorize?" + url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"scope":                 {spotify.Scopes},
		"state":                 {state},
	}.Encode()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", redirect, err)
	}
	done := make(chan error, 1)
	var once sync.Once
	finish := func(err error) { once.Do(func() { done <- err }) }
	hc := &http.Client{Timeout: 15 * time.Second}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/callback" || q.Get("state") != state {
			// Stray requests (favicon, editors probing the port) are not answers.
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if e := q.Get("error"); e != "" {
			fmt.Fprintln(w, "Вход отменён. Окно можно закрыть.")
			finish(fmt.Errorf("Spotify refused sign-in: %s", e))
			return
		}
		tok, err := spotify.Exchange(r.Context(), hc, spotify.DefaultAccountsURL, clientID, q.Get("code"), redirect, verifier)
		if err == nil {
			err = spotify.SaveToken(out, tok)
		}
		if err != nil {
			http.Error(w, "Не удалось получить токен, подробности в терминале.", http.StatusInternalServerError)
			finish(err)
			return
		}
		fmt.Fprintln(w, "Готово, вход выполнен. Окно можно закрыть.")
		finish(nil)
	})}
	go srv.Serve(ln)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	fmt.Printf("Откройте ссылку и нажмите «Принимаю»:\n\n%s\n\n", link)
	if browser {
		openBrowser(link)
	}
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-time.After(5 * time.Minute):
		return errors.New("no answer from the browser in 5 minutes")
	}
	fmt.Printf("Токен записан в %s.\nСкопируйте его на колонку рядом с config.json Quasar Bridge, например:\n\n  scp -P 2222 %s root@<IP колонки>:/data/opt/quasar-bridge/\n", out, out)
	return nil
}

func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func openBrowser(link string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	_ = cmd.Start()
}
