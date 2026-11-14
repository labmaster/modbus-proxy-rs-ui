package main

// Modbus TCP test query (FC03/FC04) through a proxy port on 127.0.0.1.

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

type ModbusRequest struct {
	Port      int `json:"port"`
	Unit      int `json:"unit"`
	FC        int `json:"fc"`
	Address   int `json:"address"`
	Count     int `json:"count"`
	TimeoutMs int `json:"timeoutMs"`
}

type RegValue struct {
	Address int    `json:"address"`
	Hex     string `json:"hex"`
	U16     uint16 `json:"u16"`
	I16     int16  `json:"i16"`
}

type PairValue struct {
	Address int    `json:"address"`
	U32     uint32 `json:"u32"`     // ABCD (high word first)
	I32     int32  `json:"i32"`     // ABCD
	F32     string `json:"f32"`     // ABCD
	U32Swap uint32 `json:"u32Swap"` // CDAB (low word first)
	I32Swap int32  `json:"i32Swap"`
	F32Swap string `json:"f32Swap"`
}

type ModbusResult struct {
	OK            bool        `json:"ok"`
	Error         string      `json:"error,omitempty"`
	Warning       string      `json:"warning,omitempty"`
	ExceptionCode int         `json:"exceptionCode,omitempty"`
	ExceptionText string      `json:"exceptionText,omitempty"`
	RequestHex    string      `json:"requestHex"`
	ResponseHex   string      `json:"responseHex,omitempty"`
	ConnectMs     float64     `json:"connectMs"`
	ResponseMs    float64     `json:"responseMs"`
	Registers     []RegValue  `json:"registers,omitempty"`
	Pairs         []PairValue `json:"pairs,omitempty"`
}

var exceptionTexts = map[byte]string{
	0x01: "Illegal function – the device does not support this function code",
	0x02: "Illegal data address",
	0x03: "Illegal data value or quantity",
	0x04: "Server device failure",
	0x05: "Acknowledge, processing still in progress",
	0x06: "Server device busy",
	0x08: "Memory parity error",
	0x0A: "Gateway path unavailable",
	0x0B: "Gateway target device failed to respond",
}

func (q *ModbusRequest) validate() error {
	switch {
	case q.Unit < 0 || q.Unit > 255:
		return errors.New("slave ID must be between 0 and 255")
	case q.FC != 3 && q.FC != 4:
		return errors.New("only FC03 and FC04 are supported")
	case q.Address < 0 || q.Address > 65535:
		return errors.New("register must be between 0 and 65535")
	case q.Count < 1 || q.Count > 125:
		return errors.New("count must be between 1 and 125")
	case q.Address+q.Count > 65536:
		return errors.New("register + count exceeds 65535")
	}
	if q.TimeoutMs == 0 {
		q.TimeoutMs = 3000
	}
	if q.TimeoutMs < 100 || q.TimeoutMs > 15000 {
		return errors.New("timeout must be between 100 and 15000 ms")
	}
	return nil
}

func hexSpaced(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, " ")
}

func msSince(t time.Time) float64 {
	return math.Round(float64(time.Since(t).Microseconds())/10) / 100
}

func fmtFloat(f float32) string {
	switch {
	case math.IsNaN(float64(f)):
		return "NaN"
	case math.IsInf(float64(f), 1):
		return "+Inf"
	case math.IsInf(float64(f), -1):
		return "-Inf"
	}
	return strconv.FormatFloat(float64(f), 'g', 7, 32)
}

func modbusQuery(q ModbusRequest) ModbusResult {
	var res ModbusResult
	var tidb [2]byte
	_, _ = rand.Read(tidb[:])
	tid := binary.BigEndian.Uint16(tidb[:])

	req := []byte{
		byte(tid >> 8), byte(tid), 0, 0, 0, 6,
		byte(q.Unit), byte(q.FC),
		byte(q.Address >> 8), byte(q.Address),
		byte(q.Count >> 8), byte(q.Count),
	}
	res.RequestHex = hexSpaced(req)
	timeout := time.Duration(q.TimeoutMs) * time.Millisecond

	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", q.Port), timeout)
	if err != nil {
		res.Error = fmt.Sprintf("Cannot connect to proxy port %d: %v", q.Port, err)
		return res
	}
	defer conn.Close()
	res.ConnectMs = msSince(t0)
	_ = conn.SetDeadline(time.Now().Add(timeout))

	t1 := time.Now()
	if _, err := conn.Write(req); err != nil {
		res.Error = "Sending failed: " + err.Error()
		return res
	}
	hdr := make([]byte, 7)
	if n, err := io.ReadFull(conn, hdr); err != nil {
		res.ResponseMs = msSince(t1)
		if n > 0 {
			res.ResponseHex = hexSpaced(hdr[:n])
		}
		res.Error = describeReadErr(err, q.TimeoutMs)
		return res
	}
	length := int(binary.BigEndian.Uint16(hdr[4:6]))
	if length < 2 || length > 254 {
		res.ResponseMs = msSince(t1)
		res.ResponseHex = hexSpaced(hdr)
		res.Error = fmt.Sprintf("Invalid length %d in the response (not Modbus TCP?)", length)
		return res
	}
	pdu := make([]byte, length-1)
	n, err := io.ReadFull(conn, pdu)
	res.ResponseMs = msSince(t1)
	res.ResponseHex = hexSpaced(append(append([]byte{}, hdr...), pdu[:n]...))
	if err != nil {
		res.Error = describeReadErr(err, q.TimeoutMs)
		return res
	}

	var warn []string
	if binary.BigEndian.Uint16(hdr[0:2]) != tid {
		warn = append(warn, "transaction ID of the response does not match the request")
	}
	if hdr[2] != 0 || hdr[3] != 0 {
		warn = append(warn, "protocol ID is not 0")
	}
	if int(hdr[6]) != q.Unit {
		warn = append(warn, fmt.Sprintf("response comes from slave ID %d", hdr[6]))
	}
	res.Warning = strings.Join(warn, "; ")

	fc := pdu[0]
	if fc == byte(q.FC)|0x80 {
		if len(pdu) < 2 {
			res.Error = "Exception response without exception code"
			return res
		}
		code := pdu[1]
		res.ExceptionCode = int(code)
		res.ExceptionText = exceptionTexts[code]
		if res.ExceptionText == "" {
			res.ExceptionText = "unknown exception code"
		}
		res.Error = fmt.Sprintf("Modbus exception %02X: %s", code, res.ExceptionText)
		return res
	}
	if int(fc) != q.FC {
		res.Error = fmt.Sprintf("Response has function code %02X instead of %02X", fc, q.FC)
		return res
	}
	if len(pdu) < 2 {
		res.Error = "Response too short"
		return res
	}
	bc := int(pdu[1])
	if bc != 2*q.Count || len(pdu) != 2+bc {
		res.Error = fmt.Sprintf("Byte count mismatch: %d bytes reported, %d expected", bc, 2*q.Count)
		return res
	}
	data := pdu[2:]
	for i := 0; i < q.Count; i++ {
		v := binary.BigEndian.Uint16(data[2*i:])
		res.Registers = append(res.Registers, RegValue{Address: q.Address + i, Hex: fmt.Sprintf("%04X", v), U16: v, I16: int16(v)})
	}
	for i := 0; i+1 < q.Count; i += 2 {
		hi, lo := uint32(res.Registers[i].U16), uint32(res.Registers[i+1].U16)
		abcd, cdab := hi<<16|lo, lo<<16|hi
		res.Pairs = append(res.Pairs, PairValue{
			Address: q.Address + i,
			U32:     abcd, I32: int32(abcd), F32: fmtFloat(math.Float32frombits(abcd)),
			U32Swap: cdab, I32Swap: int32(cdab), F32Swap: fmtFloat(math.Float32frombits(cdab)),
		})
	}
	res.OK = true
	return res
}

func describeReadErr(err error, timeoutMs int) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("No response within %d ms – device or gateway does not respond", timeoutMs)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "The proxy closed the connection without a (complete) response – device unreachable?"
	}
	return "Read error: " + err.Error()
}
