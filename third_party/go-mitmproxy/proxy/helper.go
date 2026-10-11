package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"

	log "github.com/sirupsen/logrus"
)

func expectedDisconnect(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure)
}

func (p *Proxy) logError(entry *log.Entry, err error, message string) {
	if p.isClosing() && expectedDisconnect(err) {
		if message == "" {
			entry.Debug(err)
		} else {
			entry.Debugf("%s: %v", message, err)
		}
		return
	}
	if message == "" {
		entry.Error(err)
	} else {
		entry.Errorf("%s: %v", message, err)
	}
}

var normalErrMsgs []string = []string{
	"read: connection reset by peer",
	"write: broken pipe",
	"i/o timeout",
	"net/http: TLS handshake timeout",
	"io: read/write on closed pipe",
	"connect: connection refused",
	"connect: connection reset by peer",
	"use of closed network connection",
}

// 仅打印预料之外的错误信息
func logErr(log *log.Entry, err error) (loged bool) {
	msg := err.Error()

	for _, str := range normalErrMsgs {
		if strings.Contains(msg, str) {
			log.Debug(err)
			return
		}
	}

	log.Error(err)
	loged = true
	return
}

// 转发流量
func transfer(log *log.Entry, server, client io.ReadWriteCloser) {
	errChan := make(chan error, 2)
	go func() {
		_, err := io.Copy(server, client)
		log.Debugln("client copy end", err)
		errChan <- err
	}()
	go func() {
		_, err := io.Copy(client, server)
		log.Debugln("server copy end", err)
		errChan <- err
	}()
	err := <-errChan
	server.Close()
	client.Close()
	second := <-errChan
	if err != nil {
		logErr(log, err)
	}
	if second != nil {
		logErr(log, second)
	}
}

func httpError(w http.ResponseWriter, error string, code int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`) // Indicates that the proxy server requires client credentials
	w.WriteHeader(code)
	fmt.Fprintln(w, error)
}
