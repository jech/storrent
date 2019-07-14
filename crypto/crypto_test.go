package crypto

import (
	"time"
	"sync"
	"io"
	"net"
	"testing"
)

var options1 = &Options{
	AllowCryptoHandshake: true,
	AllowEncryption: true,
	ForceEncryption: true,
}

var options0 = &Options{
	AllowCryptoHandshake: true,
	AllowEncryption: false,
	ForceEncryption: false,
}

func testHandshake(t *testing.T, options *Options) {
	client, server := net.Pipe()

	clientIa := make([]byte, 68)
	for i := range clientIa {
		clientIa[i] = byte(i)
	}

	clientSkey := make([]byte, 20)
	for i := range clientSkey {
		clientSkey[i] = byte(i)
	}

	clientData := make([]byte, 5123)
	for i := range clientData {
		clientData[i] = byte(i)
	}

	serverData := make([]byte, 6385)
	for i := range serverData {
		serverData[i] = byte(42-i)
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		client.SetDeadline(time.Now().Add(5 * time.Second))
		eclient, buf, err :=
			ClientHandshake(client, clientSkey, clientIa, options)
		if err != nil {
			t.Errorf("ClientHandshake: %v", err)
			client.Close()
			return
		}
		defer eclient.Close()
		n, err := eclient.Write(clientData)
		if err != nil || n != len(clientData) {
			t.Errorf("Client Write: %v %v", n, err)
			return
		}
		data := make([]byte, len(serverData) - len(buf))
		n, err = io.ReadFull(eclient, data[len(buf):])
		if err != nil || n != len(data) {
			t.Errorf("Client Read: %v %v", n, err)
			return
		}
		data = append(buf, data...)
		if !eq(data, serverData) {
			t.Errorf("Client data mismatch")
			return
		}
	}()

	server.SetDeadline(time.Now().Add(5 * time.Second))

	head := make([]byte, 7)
	n, err := server.Read(head)
	if err != nil {
		server.Close()
		t.Fatalf("Read: %v", err)
	}
	eserver, skey, _, err := ServerHandshake(
		server,	head, [][]byte{clientSkey, make([]byte, 20)}, options)
	if err != nil {
		server.Close()
		t.Fatalf("ServerHandshake: %v", err)
	}

	defer eserver.Close()

	if len(skey) != len(clientSkey) || !eq(skey, clientSkey) {
		t.Fatalf("Skey mismatch: %v /= %v", skey, clientSkey)
	}

	data := make([]byte, len(clientData))
	n, err = io.ReadFull(eserver, data)
	if err != nil || n != len(data) {
		t.Fatalf("Server Read: %v %v", n, err)
	}
	if !eq(data, clientData) {
		t.Fatalf("Server data mismatch")
	}

	n, err = eserver.Write(serverData)
	if err != nil || n != len(serverData) {
		t.Fatalf("Server Write: %v %v", n, err)
	}
	err = eserver.Close()
	if err != nil {
		t.Fatalf("Server close: %v", err)
	}
	wg.Wait()
}

func TestHandshake(t *testing.T) {
	t.Run("encrypted", func(t *testing.T) { testHandshake(t, options1) })
	t.Run("unencrypted", func(t *testing.T) { testHandshake(t, options0) })
}

func BenchmarkPipe(b *testing.B) {
	client, server := net.Pipe()
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		defer client.Close()
		for i := 0; i < count; i++ {
			_, err := client.Write(data)
			if err != nil {
				b.Errorf("Client Write: %v", err)
			}
		}
		err := client.Close()
		if err != nil {
			b.Errorf("Client close: %v", err)
			return
		}
	}(b.N)

	defer server.Close()
	server.SetDeadline(time.Now().Add(5 * time.Second))

	buf := make([]byte, 4096)
	for i := 0; i < b.N; i++ {
		_, err := io.ReadFull(server, buf)
		if err != nil {
			b.Fatalf("Server Read: %v", err)
		}
	}
	wg.Wait()
}

func BenchmarkHandshake(b *testing.B) {
	skey := make([]byte, 20)
	for i := 0; i < b.N; i++ {
		client, server := net.Pipe()
		go func() {
			client.SetDeadline(time.Now().Add(5 * time.Second))
			eclient, buf, err :=
				ClientHandshake(client, skey, []byte{}, options1)
			if err != nil {
				b.Errorf("ClientHandshake: %v", err)
				client.Close()
				return
			}
			defer eclient.Close()
			if len(buf) > 0 {
				b.Errorf("ClientHandshake: extra data")
				return
			}
		}()

		server.SetDeadline(time.Now().Add(5 * time.Second))

		eserver, _, _, err :=
			ServerHandshake(server, []byte{}, [][]byte{skey},
			options1)
		buf := make([]byte, 1)
		n, err := eserver.Read(buf)
		if n != 0 || err != io.EOF {
			eserver.Close()
			b.Fatalf("server read: %v", err)
		}
		eserver.Close()
	}
}


func BenchmarkClient(b *testing.B) {
	client, server := net.Pipe()
	skey := make([]byte, 20)
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		client.SetDeadline(time.Now().Add(5 * time.Second))
		eclient, buf, err :=
			ClientHandshake(client, skey, []byte{}, options1)
		if err != nil {
			b.Errorf("ClientHandshake: %v", err)
			client.Close()
			return
		}
		defer eclient.Close()
		if len(buf) > 0 {
			b.Errorf("ClientHandshake: extra data")
			return
		}
		for i := 0; i < count; i++ {
			_, err = eclient.Write(data)
			if err != nil {
				b.Errorf("Client Write: %v", err)
				return
			}
		}
	}(b.N)

	server.SetDeadline(time.Now().Add(5 * time.Second))

	eserver, _, _, err :=
		ServerHandshake(server, []byte{}, [][]byte{skey}, options1)
	if err != nil {
		server.Close()
		b.Fatalf("ServerHandshake: %v", err)
	}
	defer eserver.Close()
	buf := make([]byte, 4096)
	for i := 0; i < b.N; i++ {
		_, err := io.ReadFull(eserver, buf)
		if err != nil {
			b.Fatalf("Server Read: %v", err)
		}
	}
	wg.Wait()
}

func BenchmarkServer(b *testing.B) {
	client, server := net.Pipe()
	skey := make([]byte, 20)
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		client.SetDeadline(time.Now().Add(5 * time.Second))
		eclient, buf1, err :=
			ClientHandshake(client, skey, []byte{}, options1)
		if err != nil {
			b.Errorf("ClientHandshake: %v", err)
			client.Close()
			return
		}
		defer eclient.Close()
		if len(buf1) > 0 {
			b.Errorf("ClientHandshake: extra data")
			return
		}
		buf := make([]byte, 4096)
		for i := 0; i < count; i++ {
			_, err := io.ReadFull(eclient, buf)
			if err != nil {
				b.Errorf("Client Read: %v", err)
				return
			}
		}
	}(b.N)

	server.SetDeadline(time.Now().Add(5 * time.Second))

	eserver, _, _, err :=
		ServerHandshake(server, []byte{}, [][]byte{skey}, options1)
	if err != nil {
		server.Close()
		b.Fatalf("ServerHandshake: %v", err)
	}
	defer eserver.Close()
	for i := 0; i < b.N; i++ {
		_, err = eserver.Write(data)
		if err != nil {
			b.Fatalf("Server Write: %v", err)
		}
	}
	wg.Wait()
}
