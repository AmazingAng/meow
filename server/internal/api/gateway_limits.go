package api

import (
	"net/http"
	"net/netip"
	"time"
)

const DefaultGatewayMaxConcurrentPushes = 0 // unlimited unless the operator opts in

// Share allowance across address aliases and an IPv6 client's interface
// addresses. Proxy selection still follows the explicitly configured trust.
func gatewayClientIP(r *http.Request, trustProxy bool) string {
	raw := clientIP(r, trustProxy)
	address, err := netip.ParseAddr(raw)
	if err != nil {
		return raw
	}
	address = address.Unmap()
	if address.Is6() {
		return netip.PrefixFrom(address, 64).Masked().String()
	}
	return address.String()
}

// Never queue blocked push requests or spend route quota when all provider
// slots are in use. Existing clients can retry using the normal 429 contract.
func (g *gateway) admitPush(w http.ResponseWriter, r *http.Request) bool {
	if r.Context().Err() != nil {
		writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled")
		return false
	}
	if g.pushSlots == nil {
		return true
	}
	select {
	case g.pushSlots <- struct{}{}:
		return true
	default:
		tooManyRequests(w, time.Second)
		return false
	}
}
