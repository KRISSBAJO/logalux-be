package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

func (s *Server) ownsRecord(r *http.Request, table, id string) bool {
	if table != "orders" && table != "bookings" {
		return false
	}
	c, ok := s.customerFrom(r.Context(), bearer(r))
	if !ok {
		return false
	}
	var owner bool
	err := s.pool.QueryRow(r.Context(), "select exists(select 1 from "+table+" where id::text=$1 and user_id=$2)", id, c.ID).Scan(&owner)
	return err == nil && owner
}

func publicCalendarIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() &&
		!(&net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}).Contains(ip)
}

func calendarTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Never route calendar URLs through an environment proxy that can bypass our dial checks.
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialCalendar(ctx, network, address, net.DefaultResolver.LookupIPAddr, (&net.Dialer{Timeout: 10 * time.Second}).DialContext)
	}
	return tr
}

func dialCalendar(ctx context.Context, network, address string, lookup func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("calendar host could not be resolved")
	}
	for _, addr := range ips {
		if !publicCalendarIP(addr.IP) {
			return nil, fmt.Errorf("calendar host is not public")
		}
	}
	// Use the validated numeric address. TLS still verifies the original hostname.
	var last error
	for _, addr := range ips {
		conn, e := dial(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if e == nil {
			return conn, nil
		}
		last = e
	}
	return nil, last
}

func verifiedContact(value string, verified bool) string {
	if verified {
		return value
	}
	return ""
}

func canLinkClient(c Customer, phone string) bool {
	return c.ID != "" && c.PhoneVerified && c.Phone != "" && c.Phone == phone
}
