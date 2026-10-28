package agent

import (
	"testing"
)

func TestParsePingLatency(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{
			name: "Linux ping output standard",
			output: `PING 202.96.209.133 (202.96.209.133) 56(84) bytes of data.
64 bytes from 202.96.209.133: icmp_seq=1 ttl=245 time=142.5 ms
64 bytes from 202.96.209.133: icmp_seq=2 ttl=245 time=141.8 ms

--- 202.96.209.133 ping statistics ---
2 packets transmitted, 2 received, 0% packet loss, time 1001ms
rtt min/avg/max/mdev = 141.800/142.150/142.500/0.350 ms`,
			want: 142,
		},
		{
			name: "Windows ping output English",
			output: `Pinging 202.96.209.133 with 32 bytes of data:
Reply from 202.96.209.133: bytes=32 time=138ms TTL=53
Reply from 202.96.209.133: bytes=32 time=136ms TTL=53

Ping statistics for 202.96.209.133:
    Packets: Sent = 2, Received = 2, Lost = 0 (0% loss),
Approximate round trip times in milli-seconds:
    Minimum = 136ms, Maximum = 138ms, Average = 137ms`,
			want: 137,
		},
		{
			name: "Windows ping output Chinese",
			output: `正在 Ping 202.96.209.133 具有 32 字节的数据:
来自 202.96.209.133 的回复: 字节=32 时间=12ms TTL=53
来自 202.96.209.133 的回复: 字节=32 时间=10ms TTL=53

202.96.209.133 的 Ping 统计信息:
    数据包: 已发送 = 2，已接收 = 2，丢失 = 0 (0% 丢失)，
往返行程的估计时间(以毫秒为单位):
    最短 = 10ms，最长 = 12ms，平均 = 11ms`,
			want: 11,
		},
		{
			name: "Single ping reply time",
			output: `64 bytes from 210.22.97.1: icmp_seq=1 ttl=241 time=24.3 ms`,
			want: 24,
		},
		{
			name: "Timeout ping",
			output: `Request timed out.
Packets: Sent = 2, Received = 0, Lost = 2 (100% loss)`,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePingLatency(tt.output)
			if got != tt.want {
				t.Errorf("parsePingLatency() = %v, want %v", got, tt.want)
			}
		})
	}
}
