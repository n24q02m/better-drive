package cleanup

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

type OwnerRiskBrokerAuthority interface {
	OwnerRiskSnapshotAuthority
	OwnerRiskAuthority
}

type OwnerRiskPeerVerifier func(*http.Request) error

type OwnerRiskHTTPHandler struct {
	authority  OwnerRiskBrokerAuthority
	verifyPeer OwnerRiskPeerVerifier
}

func NewOwnerRiskHTTPHandler(authority OwnerRiskBrokerAuthority, verifyPeer OwnerRiskPeerVerifier) (*OwnerRiskHTTPHandler, error) {
	if authority == nil {
		return nil, errors.New("owner-risk broker authority is required")
	}
	if verifyPeer == nil {
		return nil, errors.New("owner-risk broker peer verifier is required")
	}
	return &OwnerRiskHTTPHandler{authority: authority, verifyPeer: verifyPeer}, nil
}

// RequireMTLSOwnerRiskPeer accepts only a TLS request whose leaf certificate is
// present in a chain verified by the server's configured client CA pool.
func RequireMTLSOwnerRiskPeer(request *http.Request) error {
	if request == nil || request.TLS == nil || len(request.TLS.PeerCertificates) == 0 || len(request.TLS.VerifiedChains) == 0 {
		return errors.New("verified mTLS peer is required")
	}
	leaf := request.TLS.PeerCertificates[0]
	for _, chain := range request.TLS.VerifiedChains {
		if len(chain) > 0 && leaf.Equal(chain[0]) {
			return nil
		}
	}
	return errors.New("mTLS peer does not match a verified chain")
}

func (handler *OwnerRiskHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setOwnerRiskResponseHeaders(writer)
	if handler == nil || handler.authority == nil || handler.verifyPeer == nil {
		writeOwnerRiskHTTPError(writer, http.StatusServiceUnavailable)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeOwnerRiskHTTPError(writer, http.StatusMethodNotAllowed)
		return
	}
	if len(request.Header.Values("Authorization")) != 0 {
		writeOwnerRiskHTTPError(writer, http.StatusUnauthorized)
		return
	}
	if err := handler.verifyPeer(request); err != nil {
		writeOwnerRiskHTTPError(writer, http.StatusUnauthorized)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") || request.Header.Get("Content-Encoding") != "" {
		writeOwnerRiskHTTPError(writer, http.StatusUnsupportedMediaType)
		return
	}
	if request.ContentLength > maxOwnerRiskBrokerBodyBytes {
		writeOwnerRiskHTTPError(writer, http.StatusRequestEntityTooLarge)
		return
	}

	switch request.URL.Path {
	case "/v1/owner-risk/snapshot":
		var input OwnerRiskSnapshotRequest
		if !decodeOwnerRiskHTTPRequest(writer, request, &input) {
			return
		}
		output, err := handler.authority.SnapshotOwnerRisk(request.Context(), input)
		if err != nil {
			writeOwnerRiskHTTPError(writer, http.StatusConflict)
			return
		}
		writeOwnerRiskHTTPJSON(writer, output)
	case "/v1/owner-risk/claim":
		var input OwnerRiskClaimRequest
		if !decodeOwnerRiskHTTPRequest(writer, request, &input) {
			return
		}
		output, err := handler.authority.ClaimOwnerRisk(request.Context(), input)
		if err != nil {
			writeOwnerRiskHTTPError(writer, http.StatusConflict)
			return
		}
		writeOwnerRiskHTTPJSON(writer, output)
	case "/v1/owner-risk/recheck":
		var input OwnerRiskFenceRequest
		if !decodeOwnerRiskHTTPRequest(writer, request, &input) {
			return
		}
		output, err := handler.authority.RecheckOwnerRisk(request.Context(), input)
		if err != nil {
			writeOwnerRiskHTTPError(writer, http.StatusConflict)
			return
		}
		writeOwnerRiskHTTPJSON(writer, output)
	case "/v1/owner-risk/settle":
		var input OwnerRiskSettlementRequest
		if !decodeOwnerRiskHTTPRequest(writer, request, &input) {
			return
		}
		output, err := handler.authority.SettleOwnerRisk(request.Context(), input)
		if err != nil {
			writeOwnerRiskHTTPError(writer, http.StatusConflict)
			return
		}
		writeOwnerRiskHTTPJSON(writer, output)
	default:
		writeOwnerRiskHTTPError(writer, http.StatusNotFound)
	}
}

func decodeOwnerRiskHTTPRequest(writer http.ResponseWriter, request *http.Request, output any) bool {
	body := http.MaxBytesReader(writer, request.Body, maxOwnerRiskBrokerBodyBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeOwnerRiskHTTPError(writer, http.StatusRequestEntityTooLarge)
		} else {
			writeOwnerRiskHTTPError(writer, http.StatusBadRequest)
		}
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeOwnerRiskHTTPError(writer, http.StatusBadRequest)
		return false
	}
	return true
}

func writeOwnerRiskHTTPJSON(writer http.ResponseWriter, value any) {
	body, err := json.Marshal(value)
	if err != nil || len(body) > maxOwnerRiskBrokerBodyBytes {
		writeOwnerRiskHTTPError(writer, http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(append(body, '\n'))
}

func writeOwnerRiskHTTPError(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, "{\"error\":\"request rejected\"}\n")
}

func setOwnerRiskResponseHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}
