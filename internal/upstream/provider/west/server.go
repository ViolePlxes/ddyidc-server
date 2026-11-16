package west

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ddyidc/internal/upstream/provider/contracts"
)

// The vendor mixes numeric and quoted scalars across endpoints and versions, so
// every number is decoded as json.Number and converted at the edge.
func dec(n json.Number) float64 {
	v, err := n.Float64()
	if err != nil {
		return 0
	}
	return v
}

func text(n json.Number) string { return string(n) }

type balanceData struct {
	Balance json.Number `json:"balance"`
	Frozen  json.Number `json:"freezemoney"`
}

func (p *Provider) balance(ctx context.Context) (available float64, frozen float64, err error) {
	var data balanceData
	if err := p.get(ctx, "/info/", map[string]string{"act": "checkbalance"}, &data); err != nil {
		return 0, 0, err
	}
	return dec(data.Balance) - dec(data.Frozen), dec(data.Frozen), nil
}

type roomEntry struct {
	RoomID json.Number   `json:"roomid"`
	Name   string        `json:"roomname"`
	Remark string        `json:"roomremark"`
	DDoS   []json.Number `json:"ddos"`
}

type osEntry struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	System  string `json:"system"`
	Bit     string `json:"bit"`
	Account string `json:"account"`
	Port    string `json:"port"`
}

type baseConfig struct {
	CPU  []json.Number `json:"cpu"`
	RAM  []json.Number `json:"ram"`
	Room []roomEntry   `json:"room"`
	OS   []osEntry     `json:"os"`
}

func (p *Provider) config(ctx context.Context) (*baseConfig, error) {
	var data baseConfig
	if err := p.get(ctx, "/server/config/", map[string]string{"act": "base"}, &data); err != nil {
		return nil, err
	}
	if len(data.Room) == 0 {
		return nil, errNoRooms
	}
	return &data, nil
}

var errNoRooms = &westError{"西部数码未返回可用机房，暂时无法销售"}

type westError struct{ message string }

func (e *westError) Error() string { return e.message }

type priceData struct {
	Price     json.Number `json:"price"`
	FullPrice json.Number `json:"fullprice"`
}

// priceQuery is the documented /server/create/?act=createprice shape. The same
// fields are accepted by act=create, which is why both share it.
type priceQuery struct {
	ProdType    int
	CPU         float64
	RAM         float64
	OSDisk      int
	DataDisk    int
	Bandwidth   int
	Room        int
	DDoS        int
	DiskType    string
	SnapshotAdv int
	CC          int
	ServiceType string
	// Units counts pay periods; it is not a month count. Keep it at 1 and let
	// PayMethod choose the period.
	Units     int
	PayMethod int
	Trial     bool
}

func (q priceQuery) params(act string) map[string]string {
	units := q.Units
	if units <= 0 {
		units = 1
	}
	params := map[string]string{
		"act":         act,
		"prodtype":    strconv.Itoa(q.ProdType),
		"cpu":         strconv.FormatFloat(q.CPU, 'f', -1, 64),
		"ram":         strconv.FormatFloat(q.RAM, 'f', -1, 64),
		"osdata":      strconv.Itoa(q.OSDisk),
		"data":        strconv.Itoa(q.DataDisk),
		"flux":        strconv.Itoa(q.Bandwidth),
		"room":        strconv.Itoa(q.Room),
		"ddos":        strconv.Itoa(q.DDoS),
		"disktype":    q.DiskType,
		"snapadv":     strconv.Itoa(q.SnapshotAdv),
		"cc":          strconv.Itoa(q.CC),
		"servicetype": q.ServiceType,
		// The interface can only sell prepaid cycles on bandwidth billing.
		"billtype":  "0",
		"metertype": "0",
		"renewtime": strconv.Itoa(units),
		"paymethod": strconv.Itoa(q.PayMethod),
	}
	if q.Trial {
		params["paytype"] = "1"
	} else {
		params["paytype"] = "0"
	}
	return params
}

func (p *Provider) createPrice(ctx context.Context, q priceQuery) (cost float64, full float64, err error) {
	var data priceData
	if err := p.get(ctx, "/server/create/", q.params("createprice"), &data); err != nil {
		return 0, 0, err
	}
	return dec(data.Price), dec(data.FullPrice), nil
}

type createdServer struct {
	ID       json.Number `json:"id"`
	IP       string      `json:"ip"`
	Domain   string      `json:"freedomain"`
	Password string      `json:"password"`
	Expires  string      `json:"expiredate"`
}

type serverInfo struct {
	ID          json.Number `json:"id"`
	IP          string      `json:"allocateip"`
	OS          string      `json:"os"`
	OSText      string      `json:"os_text"`
	CPU         string      `json:"cpu"`
	SystemDisk  string      `json:"osharddisk"`
	TotalDisk   string      `json:"harddisk"`
	Memory      string      `json:"memory"`
	DDoS        json.Number `json:"ddos"`
	CC          json.Number `json:"cc"`
	StartTime   string      `json:"starttime"`
	Room        string      `json:"serverroom"`
	Expires     string      `json:"exptime"`
	Password    string      `json:"ramdompass"`
	State       json.Number `json:"sstate"`
	ProductID   string      `json:"p_proid"`
	Bandwidth   json.Number `json:"flux"`
	Trial       json.Number `json:"buytest"`
	DiskType    string      `json:"disktype"`
	Gateway     string      `json:"netgate"`
	Mask        string      `json:"netmask"`
	ZoneID      json.Number `json:"ebsid"`
	ZoneName    string      `json:"ebsname"`
	Port        string      `json:"port"`
	Login       string      `json:"login"`
	ExtraIPs    []string    `json:"otherip"`
	AddedServer string      `json:"addedserver"`
}

func (p *Provider) serverInfo(ctx context.Context, ip string) (*serverInfo, error) {
	var data serverInfo
	if err := p.get(ctx, "/server/manager/", map[string]string{"act": "info", "ip": ip}, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

type idToIP struct {
	IP string `json:"ip"`
}

func (p *Provider) ipOf(ctx context.Context, hostID int64) (string, error) {
	var data idToIP
	if err := p.get(ctx, "/server/", map[string]string{"act": "idtoip", "id": strconv.FormatInt(hostID, 10)}, &data); err != nil {
		return "", err
	}
	ip := strings.TrimSpace(data.IP)
	if ip == "" {
		return "", &westError{"西部数码未返回该实例的 IP"}
	}
	return ip, nil
}

type runState struct {
	State json.Number `json:"status"`
	Info  string      `json:"info"`
}

func (p *Provider) runState(ctx context.Context, ip string) (*runState, error) {
	var data runState
	if err := p.get(ctx, "/server/system/", map[string]string{"act": "status", "ip": ip}, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

type renewPriceData struct {
	Price     json.Number `json:"price"`
	FullPrice json.Number `json:"fullprice"`
	Months    json.Number `json:"renewmonth"`
	PreDay    json.Number `json:"preday"`
}

func (p *Provider) renewPrice(ctx context.Context, ip string, months, payMethod int) (*renewPriceData, error) {
	var data renewPriceData
	params := map[string]string{
		"act":       "renewprice",
		"ip":        ip,
		"renewtime": strconv.Itoa(months),
		"paymethod": strconv.Itoa(payMethod),
		"snapadv":   "0",
	}
	if err := p.get(ctx, "/server/system/", params, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

type consoleData struct {
	URL string `json:"url"`
} // consoleURL is valid for one minute; callers must not persist it.
func (p *Provider) consoleURL(ctx context.Context, ip string) (string, error) {
	var data consoleData
	if err := p.get(ctx, "/server/system/", map[string]string{"act": "console", "ip": ip}, &data); err != nil {
		return "", err
	}
	if !strings.HasPrefix(data.URL, "https://") {
		return "", &westError{"西部数码返回的控制台地址不可用"}
	}
	return data.URL, nil
}

type chartHour struct {
	Title      []string      `json:"title"`
	CPU        []json.Number `json:"cpu"`
	NetIn      []json.Number `json:"ifbytesin"`
	NetOut     []json.Number `json:"ifbytesout"`
	DiskRead   []json.Number `json:"dskbytesin"`
	DiskWrite  []json.Number `json:"dskbytesout"`
	PacketsIn  []json.Number `json:"ppsin"`
	PacketsOut []json.Number `json:"ppsout"`
}

type chartData struct {
	Hour chartHour `json:"hour"`
	Day  chartHour `json:"day"`
}

func (p *Provider) charts(ctx context.Context, ip string) (*chartData, error) {
	var data chartData
	if err := p.get(ctx, "/server/system/", map[string]string{"act": "dataview", "ip": ip}, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// renew charges the agent balance and extends one machine. It is a charged
// write: callers must send it once, after the customer's payment is secured.
func (p *Provider) renew(ctx context.Context, ip string, payMethod int) error {
	return p.get(ctx, "/server/system/", map[string]string{
		"act":       "renew",
		"ip":        ip,
		"renewtime": "1",
		"paymethod": strconv.Itoa(payMethod),
		"snapadv":   "0",
	}, nil)
}

func (p *Provider) setState(ctx context.Context, ip, state string) error {
	return p.get(ctx, "/server/system/", map[string]string{"act": "setstatus", "ip": ip, "status": state}, nil)
}

func (p *Provider) changePassword(ctx context.Context, ip, password string) error {
	return p.get(ctx, "/server/manager/", map[string]string{"act": "chgpassword", "ip": ip, "newpwd": password, "pwdtype": "s"}, nil)
}

func (p *Provider) reinstall(ctx context.Context, ip, osValue string, keepData bool) error {
	keep := "N"
	if keepData {
		keep = "Y"
	}
	return p.get(ctx, "/server/system/", map[string]string{"act": "reinstall", "ip": ip, "newos": osValue, "keepdata": keep}, nil)
}

// createServerRaw sends the charged create request and keeps the transport
// error intact so procurement can tell a vendor refusal from an unknown
// outcome. Callers outside procurement should use createServer.
func (p *Provider) createServerRaw(ctx context.Context, q priceQuery, osValue string, salePrice float64) (*createdServer, error) {
	osValue = strings.TrimSpace(osValue)
	if osValue == "" {
		return nil, errors.New("请选择要安装的操作系统")
	}
	if q.Room <= 0 {
		return nil, errors.New("缺少机房编号")
	}
	client, err := p.api()
	if err != nil {
		return nil, err
	}
	params := q.params("create")
	params["os"] = osValue
	if salePrice <= 0 {
		salePrice = 9999999
	}
	params["pricmoney"] = strconv.FormatFloat(salePrice, 'f', 2, 64)
	response, err := client.Call(ctx, http.MethodGet, "/server/create/", params)
	if err != nil {
		return nil, err
	}
	var data createdServer
	if json.Unmarshal(response.Data, &data) != nil || dec(data.ID) <= 0 {
		// The request was written and the answer is unusable: treat it as an
		// uncertain charge, never as "nothing happened".
		return nil, &contracts.ProcurementFailure{Stage: "payment", Code: "invalid_response", RequestWritten: true, FirstByte: true}
	}
	return &data, nil
}

// createServer is the read-side and test entry point: it translates transport
// errors into operator-facing Chinese messages.
func (p *Provider) createServer(ctx context.Context, q priceQuery, osValue string, salePrice float64) (*createdServer, error) {
	created, err := p.createServerRaw(ctx, q, osValue, salePrice)
	if err != nil {
		return nil, translate(err)
	}
	return created, nil
}

type serverItem struct {
	ID      json.Number `json:"id"`
	IP      string      `json:"allocateip"`
	OS      string      `json:"os"`
	Room    string      `json:"serverroom"`
	Start   string      `json:"starttime"`
	Expires string      `json:"expdate"`
	Trial   json.Number `json:"buytest"`
}

type serverPage struct {
	Total json.Number  `json:"total"`
	Items []serverItem `json:"items"`
}

// listServers is the reconciliation read after an uncertain create: it proves
// whether a machine exists without repeating the charge.
func (p *Provider) listServers(ctx context.Context, page, limit int) (*serverPage, error) {
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var data serverPage
	params := map[string]string{"act": "list", "pageno": strconv.Itoa(page), "limit": strconv.Itoa(limit)}
	if err := p.get(ctx, "/server/", params, &data); err != nil {
		return nil, err
	}
	return &data, nil
}
