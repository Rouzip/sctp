package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/h12w/go-socks5"
	"github.com/ishidawataru/sctp"
)

var bufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 32*1024)
		return &b
	},
}

func forwardConnect(backend, frontend net.Conn) {
	done := make(chan struct{}, 2)

	go bridge(backend, frontend, done)
	go bridge(frontend, backend, done)

	<-done
}

func bridge(src io.Reader, dst io.Writer, done chan struct{}) {
	defer func() {
		done <- struct{}{}
	}()

	buf := bufPool.Get().(*[]byte)
	n, err := io.CopyBuffer(dst, src, *buf)
	if err != nil {
		fmt.Printf("have put %d Byte", n)
		fmt.Println(err)
	}
	bufPool.Put(buf)
}

type MySCTP struct {
	info *sctp.SndRcvInfo
	*sctp.SCTPConn
}

func (c *MySCTP) Read(b []byte) (n int, err error) {
	n, _, err = c.SCTPConn.SCTPRead(b)
	return n, err
}

func (c *MySCTP) Write(b []byte) (n int, err error) {
	n, err = c.SCTPConn.SCTPWrite(b, c.info)
	return n, err
}

func NewMySCTP(s *sctp.SCTPConn) *MySCTP {
	ppid := 0
	s.SubscribeEvents(sctp.SCTP_EVENT_DATA_IO)
	info := &sctp.SndRcvInfo{
		Stream: uint16(ppid),
		PPID:   uint32(ppid),
	}
	return &MySCTP{
		info,
		s,
	}
}

func serveClient(conn net.Conn, bufsize int) error {
	for {
		buf := make([]byte, bufsize+128) // add overhead of SCTPSndRcvInfoWrappedConn
		n, err := conn.Read(buf)
		if err != nil {
			log.Printf("read failed: %v", err)
			return err
		}
		log.Printf("read: %d", n)
		n, err = conn.Write(buf[:n])
		if err != nil {
			log.Printf("write failed: %v", err)
			return err
		}
		log.Printf("write: %d", n)
	}
}

func main() {
	var server = flag.Bool("server", false, "")
	var ip = flag.String("ip", "0.0.0.0", "")
	var port = flag.Int("port", 0, "")
	var lport = flag.Int("lport", 0, "")
	var sndbuf = flag.Int("sndbuf", 2048, "")
	var rcvbuf = flag.Int("rcvbuf", 2048, "")

	flag.Parse()

	ips := []net.IPAddr{}

	for _, i := range strings.Split(*ip, ",") {
		if a, err := net.ResolveIPAddr("ip", i); err == nil {
			log.Printf("Resolved address '%s' to %s", i, a)
			ips = append(ips, *a)
		} else {
			log.Printf("Error resolving address '%s': %v", i, err)
		}
	}

	addr := &sctp.SCTPAddr{
		IPAddrs: ips,
		Port:    *port,
	}
	log.Printf("raw addr: %+v\n", addr.ToRawSockAddrBuf())

	if *server {
		ln, err := sctp.ListenSCTP("sctp", addr)
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		log.Printf("Listen on %s", ln.Addr())

		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Fatalf("failed to accept: %v", err)
			}
			log.Printf("Accepted Connection from RemoteAddr: %s", conn.RemoteAddr())
			wconn := sctp.NewSCTPSndRcvInfoWrappedConn(conn.(*sctp.SCTPConn))
			if *sndbuf != 0 {
				err = wconn.SetWriteBuffer(*sndbuf)
				if err != nil {
					log.Fatalf("failed to set write buf: %v", err)
				}
			}
			if *rcvbuf != 0 {
				err = wconn.SetReadBuffer(*rcvbuf)
				if err != nil {
					log.Fatalf("failed to set read buf: %v", err)
				}
			}
			*sndbuf, err = wconn.GetWriteBuffer()
			if err != nil {
				log.Fatalf("failed to get write buf: %v", err)
			}
			*rcvbuf, err = wconn.GetWriteBuffer()
			if err != nil {
				log.Fatalf("failed to get read buf: %v", err)
			}
			log.Printf("SndBufSize: %d, RcvBufSize: %d", *sndbuf, *rcvbuf)
			conf := &socks5.Config{}
			server, err := socks5.New(conf)
			go server.ServeConn(wconn)
		}

	} else {
		var laddr *sctp.SCTPAddr
		if *lport != 0 {
			laddr = &sctp.SCTPAddr{
				Port: *lport,
			}
		}
		conn, err := sctp.DialSCTP("sctp", laddr, addr)
		if err != nil {
			log.Fatalf("failed to dial: %v", err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:1090")
		if err != nil {
			fmt.Println(err)
			return
		}

		sock, err := l.Accept()
		if err != nil {
			fmt.Println(err)
			return
		}

		log.Printf("Dail LocalAddr: %s; RemoteAddr: %s", conn.LocalAddr(), conn.RemoteAddr())

		if *sndbuf != 0 {
			err = conn.SetWriteBuffer(*sndbuf)
			if err != nil {
				log.Fatalf("failed to set write buf: %v", err)
			}
		}
		if *rcvbuf != 0 {
			err = conn.SetReadBuffer(*rcvbuf)
			if err != nil {
				log.Fatalf("failed to set read buf: %v", err)
			}
		}

		*sndbuf, err = conn.GetWriteBuffer()
		if err != nil {
			log.Fatalf("failed to get write buf: %v", err)
		}
		*rcvbuf, err = conn.GetReadBuffer()
		if err != nil {
			log.Fatalf("failed to get read buf: %v", err)
		}
		log.Printf("SndBufSize: %d, RcvBufSize: %d", *sndbuf, *rcvbuf)

		mySCTP := NewMySCTP(conn)
		defer mySCTP.Close()
		// buf := make([]byte, *bufsize)
		go forwardConnect(mySCTP, sock)
	}
}
