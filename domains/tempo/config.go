package tempo

// BasePath is where the Tempo Like Supply Contract API is mounted on the RTE
// data platform, per Swagger.json.
const BasePath = "/open_api/tempo_like_supply_contract/v1"

// CalendarsPath is the only resource this API exposes.
const CalendarsPath = BasePath + "/tempo_like_calendars"

// The business error codes the calendar endpoint returns. RTE reports all of
// them as HTTP 400, so the code — not the status — is what tells a caller what
// actually went wrong. Compare with rtehttp.Code(err).
const (
	// CodeDatesPairRequired means only one of start_date/end_date was sent.
	// The client rejects this before calling out; see ErrDateRangeIncomplete.
	CodeDatesPairRequired = "TMPLIKSUPCON_TMPLIKCAL_F01"
	// CodeStartAfterEnd means start_date is later than end_date. Also caught
	// client-side; see ErrDateRangeInverted.
	CodeStartAfterEnd = "TMPLIKSUPCON_TMPLIKCAL_F02"
	// CodePeriodTooLong means the window exceeds what one call may return.
	// RTE does not document the limit, so callers that need a long history
	// should split the range and retry on this code.
	CodePeriodTooLong = "TMPLIKSUPCON_TMPLIKCAL_F03"
	// CodeEndDateInvalid means data cannot be recovered up to that end_date,
	// typically because it reaches past the last published day.
	CodeEndDateInvalid = "TMPLIKSUPCON_TMPLIKCAL_F04"
	// CodePeriodTooShort means the window is below the API's minimum.
	CodePeriodTooShort = "TMPLIKSUPCON_TMPLIKCAL_F05"
	// CodeBadDateFormat means a date did not match the documented format.
	CodeBadDateFormat = "TMPLIKSUPCON_TMPLIKCAL_F06"
	// CodeBadEnumValue means an enumerated field held an unexpected value.
	CodeBadEnumValue = "TMPLIKSUPCON_TMPLIKCAL_F07"
)
