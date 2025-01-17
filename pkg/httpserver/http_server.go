package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pkg/errors"

	"github.com/iotaledger/hive.go/core/logger"
	iotago "github.com/iotaledger/iota.go/v3"
)

const (
	MIMEApplicationVendorIOTASerializerV1 = "application/vnd.iota.serializer-v1"
	ProtocolHTTP                          = "http"
	ProtocolHTTPS                         = "https"
	ProtocolWS                            = "ws"
	ProtocolWSS                           = "wss"
)

var (
	// ErrInvalidParameter defines the invalid parameter error.
	ErrInvalidParameter = echo.NewHTTPError(http.StatusBadRequest, "invalid parameter")

	// ErrNotAcceptable defines the not acceptable error.
	ErrNotAcceptable = echo.NewHTTPError(http.StatusNotAcceptable)
)

// JSONResponse sends the JSON response with status code.
func JSONResponse(c echo.Context, statusCode int, result interface{}) error {
	return c.JSON(statusCode, result)
}

// HTTPErrorResponse defines the error struct for the HTTPErrorResponseEnvelope.
type HTTPErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// HTTPErrorResponseEnvelope defines the error response schema for node API responses.
type HTTPErrorResponseEnvelope struct {
	Error HTTPErrorResponse `json:"error"`
}

func errorHandler() func(error, echo.Context) {
	return func(err error, c echo.Context) {

		var statusCode int
		var message string

		var e *echo.HTTPError
		if errors.As(err, &e) {
			statusCode = e.Code
			message = fmt.Sprintf("%s, error: %s", e.Message, err)
		} else {
			statusCode = http.StatusInternalServerError
			message = fmt.Sprintf("internal server error. error: %s", err)
		}

		_ = c.JSON(statusCode, HTTPErrorResponseEnvelope{Error: HTTPErrorResponse{Code: strconv.Itoa(statusCode), Message: message}})
	}
}

// NewEcho returns a new Echo instance.
// It hides the banner, adds a default HTTPErrorHandler and the Recover middleware.
func NewEcho(logger *logger.Logger, onHTTPError func(err error, c echo.Context), debugRequestLoggerEnabled bool) *echo.Echo {
	e := echo.New()
	e.HideBanner = true

	apiErrorHandler := errorHandler()
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if onHTTPError != nil {
			onHTTPError(err, c)
		}
		apiErrorHandler(err, c)
	}

	e.Use(middleware.Recover())

	if debugRequestLoggerEnabled {
		e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
			LogLatency:      true,
			LogRemoteIP:     true,
			LogMethod:       true,
			LogURI:          true,
			LogUserAgent:    true,
			LogStatus:       true,
			LogError:        true,
			LogResponseSize: true,
			LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
				errString := ""
				if v.Error != nil {
					errString = fmt.Sprintf("error: \"%s\", ", v.Error.Error())
				}

				logger.Debugf("%d %s \"%s\", %sagent: \"%s\", remoteIP: %s, responseSize: %s, took: %v", v.Status, v.Method, v.URI, errString, v.UserAgent, v.RemoteIP, humanize.Bytes(uint64(v.ResponseSize)), v.Latency.Truncate(time.Millisecond))

				return nil
			},
		}))
	}

	return e
}

func GetAcceptHeaderContentType(c echo.Context, supportedContentTypes ...string) (string, error) {
	ctype := c.Request().Header.Get(echo.HeaderAccept)
	for _, supportedContentType := range supportedContentTypes {
		if strings.HasPrefix(ctype, supportedContentType) {
			return supportedContentType, nil
		}
	}

	return "", ErrNotAcceptable
}

func GetRequestContentType(c echo.Context, supportedContentTypes ...string) (string, error) {
	ctype := c.Request().Header.Get(echo.HeaderContentType)
	for _, supportedContentType := range supportedContentTypes {
		if strings.HasPrefix(ctype, supportedContentType) {
			return supportedContentType, nil
		}
	}

	return "", echo.ErrUnsupportedMediaType
}

func ParseBoolQueryParam(c echo.Context, paramName string) (bool, error) {
	return strconv.ParseBool(c.QueryParam(paramName))
}

func ParseUint32QueryParam(c echo.Context, paramName string, maxValue ...uint32) (uint32, error) {
	intString := strings.ToLower(c.QueryParam(paramName))
	if intString == "" {
		return 0, errors.WithMessagef(ErrInvalidParameter, "parameter \"%s\" not specified", paramName)
	}

	value, err := strconv.ParseUint(intString, 10, 32)
	if err != nil {
		return 0, errors.WithMessagef(ErrInvalidParameter, "invalid value: %s, error: %s", intString, err)
	}

	if len(maxValue) > 0 {
		if uint32(value) > maxValue[0] {
			return 0, errors.WithMessagef(ErrInvalidParameter, "invalid value: %s, higher than the max number %d", intString, maxValue)
		}
	}

	return uint32(value), nil
}

func ParseHexQueryParam(c echo.Context, paramName string, maxLen int) ([]byte, error) {
	param := c.QueryParam(paramName)

	paramBytes, err := iotago.DecodeHex(param)
	if err != nil {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid param: %s, error: %s", paramName, err)
	}
	if len(paramBytes) > maxLen {
		return nil, errors.WithMessage(ErrInvalidParameter, fmt.Sprintf("query parameter %s too long, max. %d bytes but is %d", paramName, maxLen, len(paramBytes)))
	}

	return paramBytes, nil
}

func ParseUnixTimestampQueryParam(c echo.Context, paramName string) (time.Time, error) {
	timestamp, err := ParseUint32QueryParam(c, paramName)
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(int64(timestamp), 0), nil
}

func ParseBech32AddressQueryParam(c echo.Context, prefix iotago.NetworkPrefix, paramName string) (iotago.Address, error) {
	addressParam := strings.ToLower(c.QueryParam(paramName))

	hrp, bech32Address, err := iotago.ParseBech32(addressParam)
	if err != nil {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid address: %s, error: %s", addressParam, err)
	}

	if hrp != prefix {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid bech32 address, expected prefix: %s", prefix)
	}

	return bech32Address, nil
}

func ParseBlockIDParam(c echo.Context, paramName string) (iotago.BlockID, error) {
	blockIDHex := strings.ToLower(c.Param(paramName))

	blockID, err := iotago.BlockIDFromHexString(blockIDHex)
	if err != nil {
		return iotago.EmptyBlockID(), errors.WithMessagef(ErrInvalidParameter, "invalid block ID: %s, error: %s", blockIDHex, err)
	}

	return blockID, nil
}

func ParseTransactionIDParam(c echo.Context, paramName string) (iotago.TransactionID, error) {
	transactionID := iotago.TransactionID{}
	transactionIDHex := strings.ToLower(c.Param(paramName))

	transactionIDBytes, err := iotago.DecodeHex(transactionIDHex)
	if err != nil {
		return transactionID, errors.WithMessagef(ErrInvalidParameter, "invalid transaction ID: %s, error: %s", transactionIDHex, err)
	}

	if len(transactionIDBytes) != iotago.TransactionIDLength {
		return transactionID, errors.WithMessagef(ErrInvalidParameter, "invalid transaction ID: %s, invalid length: %d", transactionIDHex, len(transactionIDBytes))
	}

	copy(transactionID[:], transactionIDBytes)

	return transactionID, nil
}

func ParseOutputIDParam(c echo.Context, paramName string) (iotago.OutputID, error) {
	outputIDParam := strings.ToLower(c.Param(paramName))

	outputID, err := iotago.OutputIDFromHex(outputIDParam)
	if err != nil {
		return iotago.OutputID{}, errors.WithMessagef(ErrInvalidParameter, "invalid output ID: %s, error: %s", outputIDParam, err)
	}

	return outputID, nil
}

func ParseMilestoneIndexParam(c echo.Context, paramName string) (iotago.MilestoneIndex, error) {
	milestoneIndex := strings.ToLower(c.Param(paramName))
	if milestoneIndex == "" {
		return 0, errors.WithMessagef(ErrInvalidParameter, "parameter \"%s\" not specified", paramName)
	}

	msIndex, err := strconv.ParseUint(milestoneIndex, 10, 32)
	if err != nil {
		return 0, errors.WithMessagef(ErrInvalidParameter, "invalid milestone index: %s, error: %s", milestoneIndex, err)
	}

	return iotago.MilestoneIndex(msIndex), nil
}

func ParseMilestoneIDParam(c echo.Context, paramName string) (*iotago.MilestoneID, error) {
	milestoneIDHex := strings.ToLower(c.Param(paramName))

	milestoneIDBytes, err := iotago.DecodeHex(milestoneIDHex)
	if err != nil {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid milestone ID: %s, error: %s", milestoneIDHex, err)
	}

	if len(milestoneIDBytes) != iotago.MilestoneIDLength {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid milestone ID: %s, invalid length: %d", milestoneIDHex, len(milestoneIDBytes))
	}

	var milestoneID iotago.MilestoneID
	copy(milestoneID[:], milestoneIDBytes)

	return &milestoneID, nil
}

func ParseAliasIDParam(c echo.Context, paramName string) (*iotago.AliasID, error) {
	aliasIDParam := strings.ToLower(c.Param(paramName))

	aliasIDBytes, err := iotago.DecodeHex(aliasIDParam)
	if err != nil {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid alias ID: %s, error: %s", aliasIDParam, err)
	}

	if len(aliasIDBytes) != iotago.AliasIDLength {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid alias ID: %s, error: %s", aliasIDParam, err)
	}

	var aliasID iotago.AliasID
	copy(aliasID[:], aliasIDBytes)

	return &aliasID, nil
}

func ParseNFTIDParam(c echo.Context, paramName string) (*iotago.NFTID, error) {
	nftIDParam := strings.ToLower(c.Param(paramName))

	nftIDBytes, err := iotago.DecodeHex(nftIDParam)
	if err != nil {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid NFT ID: %s, error: %s", nftIDParam, err)
	}

	if len(nftIDBytes) != iotago.NFTIDLength {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid NFT ID: %s, error: %s", nftIDParam, err)
	}

	var nftID iotago.NFTID
	copy(nftID[:], nftIDBytes)

	return &nftID, nil
}

func ParseFoundryIDParam(c echo.Context, paramName string) (*iotago.FoundryID, error) {
	foundryIDParam := strings.ToLower(c.Param(paramName))

	foundryIDBytes, err := iotago.DecodeHex(foundryIDParam)
	if err != nil {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid foundry ID: %s, error: %s", foundryIDParam, err)
	}

	if len(foundryIDBytes) != iotago.FoundryIDLength {
		return nil, errors.WithMessagef(ErrInvalidParameter, "invalid foundry ID: %s, error: %s", foundryIDParam, err)
	}

	var foundryID iotago.FoundryID
	copy(foundryID[:], foundryIDBytes)

	return &foundryID, nil
}

func GetURL(protocol string, host string, port uint16, path ...string) string {
	return fmt.Sprintf("%s://%s%s", protocol, net.JoinHostPort(host, strconv.Itoa(int(port))), strings.Join(path, "/"))
}
