package gateway

import "net/http"

const AssertionHeader = "X-GuanJin-Assertion"

func setAssertion(h http.Header, token string) {
	h.Del(AssertionHeader)
	if token != "" {
		h.Set(AssertionHeader, token)
	}
}
