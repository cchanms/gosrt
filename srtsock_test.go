// Copyright (c) 2018 CyberAgent, Inc. All rights reserved.
// https://github.com/openfresh/gosrt

// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
// https://github.com/golang/go

package gosrt

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/openfresh/gosrt/internal/testenv"
)

func BenchmarkSRT4OneShot(b *testing.B) {
	benchmarkSRT(b, false, false, "127.0.0.1:0")
}

func BenchmarkSRT4OneShotTimeout(b *testing.B) {
	benchmarkSRT(b, false, true, "127.0.0.1:0")
}

func BenchmarkSRT4Persistent(b *testing.B) {
	benchmarkSRT(b, true, false, "127.0.0.1:0")
}

func BenchmarkSRT4PersistentTimeout(b *testing.B) {
	benchmarkSRT(b, true, true, "127.0.0.1:0")
}

func BenchmarkSRT6OneShot(b *testing.B) {
	if !supportsIPv6() {
		b.Skip("ipv6 is not supported")
	}
	benchmarkSRT(b, false, false, "[::1]:0")
}

func BenchmarkSRT6OneShotTimeout(b *testing.B) {
	if !supportsIPv6() {
		b.Skip("ipv6 is not supported")
	}
	benchmarkSRT(b, false, true, "[::1]:0")
}

func BenchmarkSRT6Persistent(b *testing.B) {
	if !supportsIPv6() {
		b.Skip("ipv6 is not supported")
	}
	benchmarkSRT(b, true, false, "[::1]:0")
}

func BenchmarkSRT6PersistentTimeout(b *testing.B) {
	if !supportsIPv6() {
		b.Skip("ipv6 is not supported")
	}
	benchmarkSRT(b, true, true, "[::1]:0")
}

func benchmarkSRT(b *testing.B, persistent, timeout bool, laddr string) {
	testHookUninstaller.Do(uninstallTestHooks)

	const msgLen = 512
	conns := b.N
	numConcurrent := runtime.GOMAXPROCS(-1) * 2
	msgs := 1
	if persistent {
		conns = numConcurrent
		msgs = b.N / conns
		if msgs == 0 {
			msgs = 1
		}
		if conns > b.N {
			conns = b.N
		}
	}
	sendMsg := func(c net.Conn, buf []byte) bool {
		n, err := c.Write(buf)
		if n != len(buf) || err != nil {
			b.Log(err)
			return false
		}
		return true
	}
	recvMsg := func(c net.Conn, buf []byte) bool {
		for read := 0; read != len(buf); {
			n, err := c.Read(buf)
			read += n
			if err != nil {
				b.Log(err)
				return false
			}
		}
		return true
	}
	ln, err := Listen("srt", laddr)
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	serverSem := make(chan bool, numConcurrent)
	// Acceptor.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				break
			}
			serverSem <- true
			// Server connection.
			go func(c net.Conn) {
				defer func() {
					c.Close()
					<-serverSem
				}()
				if timeout {
					c.SetDeadline(time.Now().Add(time.Hour)) // Not intended to fire.
				}
				var buf [msgLen]byte
				for m := 0; m < msgs; m++ {
					if !recvMsg(c, buf[:]) || !sendMsg(c, buf[:]) {
						break
					}
				}
			}(c)
		}
	}()
	clientSem := make(chan bool, numConcurrent)
	for i := 0; i < conns; i++ {
		clientSem <- true
		// Client connection.
		go func() {
			defer func() {
				<-clientSem
			}()
			c, err := Dial("srt", ln.Addr().String())
			if err != nil {
				b.Log(err)
				return
			}
			defer c.Close()
			if timeout {
				c.SetDeadline(time.Now().Add(time.Hour)) // Not intended to fire.
			}
			var buf [msgLen]byte
			for m := 0; m < msgs; m++ {
				if !sendMsg(c, buf[:]) || !recvMsg(c, buf[:]) {
					break
				}
			}
		}()
	}
	for i := 0; i < numConcurrent; i++ {
		clientSem <- true
		serverSem <- true
	}
}

func BenchmarkSRT4ConcurrentReadWrite(b *testing.B) {
	benchmarkSRTConcurrentReadWrite(b, "127.0.0.1:0")
}

func BenchmarkSRT6ConcurrentReadWrite(b *testing.B) {
	if !supportsIPv6() {
		b.Skip("ipv6 is not supported")
	}
	benchmarkSRTConcurrentReadWrite(b, "[::1]:0")
}

func benchmarkSRTConcurrentReadWrite(b *testing.B, laddr string) {
	testHookUninstaller.Do(uninstallTestHooks)

	// The benchmark creates GOMAXPROCS client/server pairs.
	// Each pair creates 4 goroutines: client reader/writer and server reader/writer.
	// The benchmark stresses concurrent reading and writing to the same connection.
	// Such pattern is used in net/http and net/rpc.

	b.StopTimer()

	P := runtime.GOMAXPROCS(0)
	N := b.N / P
	W := 1000

	// Setup P client/server connections.
	clients := make([]net.Conn, P)
	servers := make([]net.Conn, P)
	ln, err := Listen("srt", laddr)
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	done := make(chan bool)
	go func() {
		for p := 0; p < P; p++ {
			s, err := ln.Accept()
			if err != nil {
				b.Error(err)
				return
			}
			servers[p] = s
		}
		done <- true
	}()
	for p := 0; p < P; p++ {
		c, err := Dial("srt", ln.Addr().String())
		if err != nil {
			b.Fatal(err)
		}
		clients[p] = c
	}
	<-done

	b.StartTimer()

	var wg sync.WaitGroup
	wg.Add(4 * P)
	for p := 0; p < P; p++ {
		// Client writer.
		go func(c net.Conn) {
			defer wg.Done()
			var buf [1]byte
			for i := 0; i < N; i++ {
				v := byte(i)
				for w := 0; w < W; w++ {
					v *= v
				}
				buf[0] = v
				_, err := c.Write(buf[:])
				if err != nil {
					b.Error(err)
					return
				}
			}
		}(clients[p])

		// Pipe between server reader and server writer.
		pipe := make(chan byte, 128)

		// Server reader.
		go func(s net.Conn) {
			defer wg.Done()
			var buf [1]byte
			for i := 0; i < N; i++ {
				_, err := s.Read(buf[:])
				if err != nil {
					b.Error(err)
					return
				}
				pipe <- buf[0]
			}
		}(servers[p])

		// Server writer.
		go func(s net.Conn) {
			defer wg.Done()
			var buf [1]byte
			for i := 0; i < N; i++ {
				v := <-pipe
				for w := 0; w < W; w++ {
					v *= v
				}
				buf[0] = v
				_, err := s.Write(buf[:])
				if err != nil {
					b.Error(err)
					return
				}
			}
			s.Close()
		}(servers[p])

		// Client reader.
		go func(c net.Conn) {
			defer wg.Done()
			var buf [1]byte
			for i := 0; i < N; i++ {
				_, err := c.Read(buf[:])
				if err != nil {
					b.Error(err)
					return
				}
			}
			c.Close()
		}(clients[p])
	}
	wg.Wait()
}

type resolveSRTAddrTest struct {
	network       string
	litAddrOrName string
	addr          *SRTAddr
	err           error
}

var resolveSRTAddrTests = []resolveSRTAddrTest{
	{"srt", "127.0.0.1:0", &SRTAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}, nil},
	{"srt4", "127.0.0.1:65535", &SRTAddr{IP: net.IPv4(127, 0, 0, 1), Port: 65535}, nil},

	{"srt", "[::1]:0", &SRTAddr{IP: net.ParseIP("::1"), Port: 0}, nil},
	{"srt6", "[::1]:65535", &SRTAddr{IP: net.ParseIP("::1"), Port: 65535}, nil},

	{"srt", "[::1%en0]:1", &SRTAddr{IP: net.ParseIP("::1"), Port: 1, Zone: "en0"}, nil},
	{"srt6", "[::1%911]:2", &SRTAddr{IP: net.ParseIP("::1"), Port: 2, Zone: "911"}, nil},

	{"", "127.0.0.1:0", &SRTAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}, nil}, // Go 1.0 behavior
	{"", "[::1]:0", &SRTAddr{IP: net.ParseIP("::1"), Port: 0}, nil},         // Go 1.0 behavior

	{"srt", ":12345", &SRTAddr{Port: 12345}, nil},

	{"http", "127.0.0.1:0", nil, net.UnknownNetworkError("http")},
}

func TestResolveSRTAddr(t *testing.T) {
	origTestHookLookupIP := testHookLookupIP
	defer func() { testHookLookupIP = origTestHookLookupIP }()
	testHookLookupIP = lookupLocalhost

	for _, tt := range resolveSRTAddrTests {
		addr, err := ResolveSRTAddr(tt.network, tt.litAddrOrName)
		if !reflect.DeepEqual(addr, tt.addr) || !reflect.DeepEqual(err, tt.err) {
			t.Errorf("ResolveSRTAddr(%q, %q) = %#v, %v, want %#v, %v", tt.network, tt.litAddrOrName, addr, err, tt.addr, tt.err)
			continue
		}
		if err == nil {
			addr2, err := ResolveSRTAddr(addr.Network(), addr.String())
			if !reflect.DeepEqual(addr2, tt.addr) || err != tt.err {
				t.Errorf("(%q, %q): ResolveSRTAddr(%q, %q) = %#v, %v, want %#v, %v", tt.network, tt.litAddrOrName, addr.Network(), addr.String(), addr2, err, tt.addr, tt.err)
			}
		}
	}
}

var srtListenerNameTests = []struct {
	net   string
	laddr *SRTAddr
}{
	{"srt4", &SRTAddr{IP: net.IPv4(127, 0, 0, 1)}},
	{"srt4", &SRTAddr{}},
	{"srt4", nil},
}

func TestSRTListenerName(t *testing.T) {
	testenv.MustHaveExternalNetwork(t)

	for _, tt := range srtListenerNameTests {
		ln, err := ListenSRT(tt.net, tt.laddr)
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		la := ln.Addr()
		if a, ok := la.(*SRTAddr); !ok || a.Port == 0 {
			t.Fatalf("got %v; expected a proper address with non-zero port number", la)
		}
	}
}

func TestIPv6LinkLocalUnicastSRT(t *testing.T) {
	testenv.MustHaveExternalNetwork(t)

	if !supportsIPv6() {
		t.Skip("IPv6 is not supported")
	}

	for i, tt := range ipv6LinkLocalUnicastSRTTests {
		ln, err := Listen(tt.network, tt.address)
		if err != nil {
			// It might return "LookupHost returned no
			// suitable address" error on some platforms.
			t.Log(err)
			continue
		}
		ls, err := (&streamListener{Listener: ln}).newLocalServer()
		if err != nil {
			t.Fatal(err)
		}
		defer ls.teardown()
		ch := make(chan error, 1)
		handler := func(ls *localServer, ln net.Listener) { transponder(ln, ch) }
		if err := ls.buildup(handler); err != nil {
			t.Fatal(err)
		}
		if la, ok := ln.Addr().(*SRTAddr); !ok || !tt.nameLookup && la.Zone == "" {
			t.Fatalf("got %v; expected a proper address with zone identifier", la)
		}

		var d Dialer
		ctx := WithOptions(context.Background(), Options("payloadsize", "32"))
		c, err := d.DialContext(ctx, tt.network, ls.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if la, ok := c.LocalAddr().(*SRTAddr); !ok || !tt.nameLookup && la.Zone == "" {
			t.Fatalf("got %v; expected a proper address with zone identifier", la)
		}
		if ra, ok := c.RemoteAddr().(*SRTAddr); !ok || !tt.nameLookup && ra.Zone == "" {
			t.Fatalf("got %v; expected a proper address with zone identifier", ra)
		}

		if _, err := c.Write([]byte("SRT OVER IPV6 LINKLOCAL TEST")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 32)
		if _, err := c.Read(b); err != nil {
			t.Fatal(err)
		}

		for err := range ch {
			t.Errorf("#%d: %v", i, err)
		}
	}
}

func TestSRTConcurrentAccept(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(4))
	ln, err := Listen("srt", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					break
				}
				c.Close()
			}
			wg.Done()
		}()
	}
	attempts := 10 * N
	fails := 0
	d := &Dialer{Timeout: 200 * time.Millisecond}
	for i := 0; i < attempts; i++ {
		c, err := d.Dial("srt", ln.Addr().String())
		if err != nil {
			fails++
		} else {
			c.Close()
		}
	}
	ln.Close()
	wg.Wait()
	if fails > attempts/9 { // see issues 7400 and 7541
		t.Fatalf("too many Dial failed: %v", fails)
	}
	if fails > 0 {
		t.Logf("# of failed Dials: %v", fails)
	}
}

func TestSRTReadWriteAllocs(t *testing.T) {
	switch runtime.GOOS {
	case "plan9":
		// The implementation of asynchronous cancelable
		// I/O on Plan 9 allocates memory.
		// See net/fd_io_plan9.go.
		t.Skipf("not supported on %s", runtime.GOOS)
	case "nacl":
		// NaCl needs to allocate pseudo file descriptor
		// stuff. See syscall/fd_nacl.go.
		t.Skipf("not supported on %s", runtime.GOOS)
	}

	ln, err := Listen("srt", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var server net.Conn
	errc := make(chan error, 1)
	go func() {
		var err error
		server, err = ln.Accept()
		errc <- err
	}()
	client, err := Dial("srt", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	var buf [128]byte
	allocs := testing.AllocsPerRun(1000, func() {
		_, err := server.Write(buf[:])
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadFull(client, buf[:])
		if err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 0 {
		t.Fatalf("got %v; want 0", allocs)
	}

	var bufwrt [128]byte
	ch := make(chan bool)
	defer close(ch)
	go func() {
		for <-ch {
			_, err := server.Write(bufwrt[:])
			errc <- err
		}
	}()
	allocs = testing.AllocsPerRun(1000, func() {
		ch <- true
		if _, err = io.ReadFull(client, buf[:]); err != nil {
			t.Fatal(err)
		}
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 0 {
		t.Fatalf("got %v; want 0", allocs)
	}
}

func TestSRTStress(t *testing.T) {
	const conns = 2
	const msgLen = 512
	msgs := int(1e4)
	if testing.Short() {
		msgs = 1e2
	}

	sendMsg := func(c net.Conn, buf []byte) bool {
		n, err := c.Write(buf)
		if n != len(buf) || err != nil {
			t.Log(err)
			return false
		}
		return true
	}
	recvMsg := func(c net.Conn, buf []byte) bool {
		for read := 0; read != len(buf); {
			n, err := c.Read(buf)
			read += n
			if err != nil {
				t.Log(err)
				return false
			}
		}
		return true
	}

	ln, err := Listen("srt", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan bool)
	// Acceptor.
	go func() {
		defer func() {
			done <- true
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				break
			}
			// Server connection.
			go func(c net.Conn) {
				defer c.Close()
				var buf [msgLen]byte
				for m := 0; m < msgs; m++ {
					if !recvMsg(c, buf[:]) || !sendMsg(c, buf[:]) {
						break
					}
				}
			}(c)
		}
	}()
	for i := 0; i < conns; i++ {
		// Client connection.
		go func() {
			defer func() {
				done <- true
			}()
			c, err := Dial("srt", ln.Addr().String())
			if err != nil {
				t.Log(err)
				return
			}
			defer c.Close()
			var buf [msgLen]byte
			for m := 0; m < msgs; m++ {
				if !sendMsg(c, buf[:]) || !recvMsg(c, buf[:]) {
					break
				}
			}
		}()
	}
	for i := 0; i < conns; i++ {
		<-done
	}
	ln.Close()
	<-done
}

func TestSRTSelfConnect(t *testing.T) {
	if runtime.GOOS == "windows" {
		// TODO(brainman): do not know why it hangs.
		t.Skip("known-broken test on windows")
	}

	ln, err := newLocalListener("srt")
	if err != nil {
		t.Fatal(err)
	}
	var d Dialer
	c, err := d.Dial(ln.Addr().Network(), ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	network := c.LocalAddr().Network()
	laddr := *c.LocalAddr().(*SRTAddr)
	c.Close()
	ln.Close()

	// Try to connect to that address repeatedly.
	n := 100000
	if testing.Short() {
		n = 1000
	}
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "netbsd", "openbsd", "plan9", "solaris", "windows":
		// Non-Linux systems take a long time to figure
		// out that there is nothing listening on localhost.
		n = 100
	}
	for i := 0; i < n; i++ {
		d.Timeout = time.Millisecond
		c, err := d.Dial(network, laddr.String())
		if err == nil {
			addr := c.LocalAddr().(*SRTAddr)
			if addr.Port == laddr.Port || addr.IP.Equal(laddr.IP) {
				t.Errorf("Dial %v should fail", addr)
			} else {
				t.Logf("Dial %v succeeded - possibly racing with other listener", addr)
			}
			c.Close()
		}
	}
}

// Test that >32-bit reads work on 64-bit systems.
// On 32-bit systems this tests that maxint reads work.
func TestSRTBig(t *testing.T) {
	if !*testSRTBig {
		t.Skip("test disabled; use -srtbig to enable")
	}

	for _, writev := range []bool{false, true} {
		t.Run(fmt.Sprintf("writev=%v", writev), func(t *testing.T) {
			ln, err := newLocalListener("srt")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()

			x := int(1 << 30)
			x = x*5 + 1<<20 // just over 5 GB on 64-bit, just over 1GB on 32-bit
			done := make(chan int)
			go func() {
				defer close(done)
				c, err := ln.Accept()
				if err != nil {
					t.Error(err)
					return
				}
				buf := make([]byte, x)
				var n int
				if writev {
					var n64 int64
					n64, err = (&net.Buffers{buf}).WriteTo(c)
					n = int(n64)
				} else {
					n, err = c.Write(buf)
				}
				if n != len(buf) || err != nil {
					t.Errorf("Write(buf) = %d, %v, want %d, nil", n, err, x)
				}
				c.Close()
			}()

			c, err := Dial("srt", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, x)
			n, err := io.ReadFull(c, buf)
			if n != len(buf) || err != nil {
				t.Errorf("Read(buf) = %d, %v, want %d, nil", n, err, x)
			}
			c.Close()
			<-done
		})
	}
}

func TestCopyPipeIntoSRT(t *testing.T) {
	ln, err := newLocalListener("srt")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	errc := make(chan error, 1)
	defer func() {
		if err := <-errc; err != nil {
			t.Error(err)
		}
	}()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		defer c.Close()

		buf := make([]byte, 100)
		n, err := io.ReadFull(c, buf)
		if err != io.ErrUnexpectedEOF || n != 2 {
			errc <- fmt.Errorf("got err=%q n=%v; want err=%q n=2", err, n, io.ErrUnexpectedEOF)
			return
		}

		errc <- nil
	}()

	c, err := Dial("srt", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	errc2 := make(chan error, 1)
	defer func() {
		if err := <-errc2; err != nil {
			t.Error(err)
		}
	}()

	defer w.Close()

	go func() {
		_, err := io.Copy(c, r)
		errc2 <- err
	}()

	// Split write into 2 packets. That makes Windows TransmitFile
	// drop second packet.
	packet := make([]byte, 1)
	_, err = w.Write(packet)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_, err = w.Write(packet)
	if err != nil {
		t.Fatal(err)
	}
}
