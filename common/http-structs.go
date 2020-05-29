package common

type InnerReq struct {
	Method      string
	Path        string
	Query       string
	Headers     map[string][]string
	BackhaulIds []string
	ChanId      uint16
}
type InnerResp struct {
	RequestId  string
	Status     string
	StatusCode int
	Headers    map[string][]string
}
