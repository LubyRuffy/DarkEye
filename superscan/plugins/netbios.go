package plugins

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"
)

func nbCheck(s *Service) {
	s.parent.TargetPort = s.port
	nbConn(s)
}

func nbConn(s *Service) {
	c := net.Dialer{Timeout: time.Duration(Config.TimeOut) * time.Millisecond}
	ctx, _ := context.WithCancel(Config.ParentCtx)
	conn, err := c.DialContext(ctx, "udp",
		fmt.Sprintf("%s:%s", s.parent.TargetIp, s.parent.TargetPort))
	if err != nil {
		return
	}
	defer conn.Close()
	this := ProbeNetbios{
		socket: conn,
		nb:     NetbiosInfo{},
	}
	if err := this.SendStatusRequest(); err != nil {
		return
	}
	if err := this.ProcessReplies(); err != nil {
		return
	}
	s.parent.Result.PortOpened = true
	s.parent.Result.ServiceName = s.name
	s.parent.Result.NetBios.Name = this.trimName(string(this.nb.statusReply.HostName[:]))
	if this.nb.nameReply.Header.RecordType == 0x20 {
		for _, a := range this.nb.nameReply.Addresses {
			net := fmt.Sprintf("%d.%d.%d.%d", a.Address[0], a.Address[1], a.Address[2], a.Address[3])
			if net == "0.0.0.0" {
				continue
			}
			s.parent.Result.NetBios.Net += net + ","
		}
		s.parent.Result.NetBios.Net = strings.TrimSuffix(s.parent.Result.NetBios.Net, ",")
	}

	if this.nb.statusReply.HWAddr != "00:00:00:00:00:00" {
		s.parent.Result.NetBios.Hw = this.nb.statusReply.HWAddr
	}
	username := this.trimName(string(this.nb.statusReply.UserName[:]))
	if len(username) > 0 {
		s.parent.Result.NetBios.UserName = username
	}
	for _, rName := range this.nb.statusReply.Names {

		tName := this.trimName(string(rName.Name[:]))
		if tName == s.parent.Result.NetBios.Name {
			continue
		}
		if rName.Flag&0x0800 != 0 {
			continue
		}
		s.parent.Result.NetBios.Domain = tName
	}
}

type NetbiosInfo struct {
	statusRecv  time.Time
	nameSent    time.Time
	nameRecv    time.Time
	statusReply NetbiosReplyStatus
	nameReply   NetbiosReplyStatus
}

type ProbeNetbios struct {
	socket net.Conn
	nb     NetbiosInfo
}

type NetbiosReplyHeader struct {
	XID             uint16
	Flags           uint16
	QuestionCount   uint16
	AnswerCount     uint16
	AuthCount       uint16
	AdditionalCount uint16
	QuestionName    [34]byte
	RecordType      uint16
	RecordClass     uint16
	RecordTTL       uint32
	RecordLength    uint16
}

type NetbiosReplyName struct {
	Name [15]byte
	Type uint8
	Flag uint16
}

type NetbiosReplyAddress struct {
	Flag    uint16
	Address [4]uint8
}

type NetbiosReplyStatus struct {
	Header    NetbiosReplyHeader
	HostName  [15]byte
	UserName  [15]byte
	Names     []NetbiosReplyName
	Addresses []NetbiosReplyAddress
	HWAddr    string
}

func (this *ProbeNetbios) ProcessReplies() error {
	buff := make([]byte, 1500)
	packet := 2
	for packet > 0 {
		packet--
		_ = this.socket.SetDeadline(time.Now().Add(time.Duration(Config.TimeOut) * time.Millisecond))
		rLen, err := this.socket.Read(buff)
		if err != nil {
			return err
		}
		reply := this.ParseReply(buff[0 : rLen-1])
		if len(reply.Names) == 0 && len(reply.Addresses) == 0 {
			continue
		}
		if reply.Header.RecordType == 0x21 {
			this.nb.statusReply = reply
			this.nb.statusRecv = time.Now()
			nTime := time.Time{}
			if this.nb.nameSent == nTime {
				this.nb.nameSent = time.Now()
				name := this.trimName(string(this.nb.statusReply.HostName[:]))
				if err = this.SendNameRequest(name); err != nil {
					return err
				}
			}
		}
		if reply.Header.RecordType == 0x20 {
			this.nb.nameReply = reply
			this.nb.nameRecv = time.Now()
			return nil
		}
	}
	return fmt.Errorf("bye bye")
}

func (this *ProbeNetbios) SendRequest(req []byte) error {
	_ = this.socket.SetDeadline(time.Now().Add(time.Duration(Config.TimeOut) * time.Millisecond))
	if _, err := this.socket.Write(req); err != nil {
		return err
	}
	return nil
}

func (this *ProbeNetbios) SendStatusRequest() error {
	return this.SendRequest(this.CreateStatusRequest())
}

func (this *ProbeNetbios) SendNameRequest(name string) error {
	return this.SendRequest(this.CreateNameRequest(name))
}

func (this *ProbeNetbios) EncodeNetbiosName(name [16]byte) [32]byte {
	encoded := [32]byte{}

	for i := 0; i < 16; i++ {
		if name[i] == 0 {
			encoded[(i*2)+0] = 'C'
			encoded[(i*2)+1] = 'A'
		} else {
			encoded[(i*2)+0] = byte((name[i] / 16) + 0x41)
			encoded[(i*2)+1] = byte((name[i] % 16) + 0x41)
		}
	}

	return encoded
}

func (this *ProbeNetbios) DecodeNetbiosName(name [32]byte) [16]byte {
	decoded := [16]byte{}

	for i := 0; i < 16; i++ {
		if name[(i*2)+0] == 'C' && name[(i*2)+1] == 'A' {
			decoded[i] = 0
		} else {
			decoded[i] = ((name[(i*2)+0] * 16) - 0x41) + (name[(i*2)+1] - 0x41)
		}
	}
	return decoded
}

func (this *ProbeNetbios) ParseReply(buff []byte) NetbiosReplyStatus {

	resp := NetbiosReplyStatus{}
	temp := bytes.NewBuffer(buff)

	_ = binary.Read(temp, binary.BigEndian, &resp.Header)

	if resp.Header.QuestionCount != 0 {
		return resp
	}

	if resp.Header.AnswerCount == 0 {
		return resp
	}

	// Names
	if resp.Header.RecordType == 0x21 {
		var rcnt uint8
		var ridx uint8
		_ = binary.Read(temp, binary.BigEndian, &rcnt)

		for ridx = 0; ridx < rcnt; ridx++ {
			name := NetbiosReplyName{}
			binary.Read(temp, binary.BigEndian, &name)
			resp.Names = append(resp.Names, name)

			if name.Type == 0x20 {
				resp.HostName = name.Name
			}

			if name.Type == 0x03 {
				resp.UserName = name.Name
			}
		}

		var hwbytes [6]uint8
		_ = binary.Read(temp, binary.BigEndian, &hwbytes)
		resp.HWAddr = fmt.Sprintf("%.2x:%.2x:%.2x:%.2x:%.2x:%.2x",
			hwbytes[0], hwbytes[1], hwbytes[2], hwbytes[3], hwbytes[4], hwbytes[5],
		)
		return resp
	}

	// Addresses
	if resp.Header.RecordType == 0x20 {
		var ridx uint16
		for ridx = 0; ridx < (resp.Header.RecordLength / 6); ridx++ {
			addr := NetbiosReplyAddress{}
			_ = binary.Read(temp, binary.BigEndian, &addr)
			resp.Addresses = append(resp.Addresses, addr)
		}
	}

	return resp
}

func (this *ProbeNetbios) CreateStatusRequest() []byte {
	return []byte{
		byte(rand.Intn(256)), byte(rand.Intn(256)),
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x20, 0x43, 0x4b, 0x41, 0x41, 0x41,
		0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41,
		0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41,
		0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41,
		0x41, 0x41, 0x41, 0x00, 0x00, 0x21, 0x00, 0x01,
	}
}

func (this *ProbeNetbios) CreateNameRequest(name string) []byte {
	nbytes := [16]byte{}
	copy(nbytes[0:15], []byte(strings.ToUpper(name)[:]))

	req := []byte{
		byte(rand.Intn(256)), byte(rand.Intn(256)),
		0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x20,
		0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41,
		0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41,
		0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41,
		0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x41,
		0x00, 0x00, 0x20, 0x00, 0x01,
	}

	encoded := this.EncodeNetbiosName(nbytes)
	copy(req[13:45], encoded[0:32])
	return req
}

func (this *ProbeNetbios) trimName(name string) string {
	return strings.TrimSpace(strings.Replace(name, "\x00", "", -1))
}

func init() {
	preServices["netbios"] = Service{
		name:  "netbios",
		port:  "137",
		check: nbCheck,
	}
}
