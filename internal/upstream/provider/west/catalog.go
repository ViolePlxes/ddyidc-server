package west

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ddyidc/internal/upstream/provider/contracts"
)

// billingCycles is the only cycle vocabulary the platform quotes.
//
// Verified against the live endpoint on 2026-11-15 (room 36, 1 core / 1G):
// paymethod selects the billing unit and renewtime counts units, it is NOT a
// month count. Sending paymethod=2&renewtime=3 buys three quarters, not three
// months, which is why every cycle is now sent as unit=1.
//
//	paymethod 0 renewtime 1 -> 60.00    (one month)
//	paymethod 2 renewtime 1 -> 206.00   (one quarter)
//	paymethod 3 renewtime 1 -> 365.00   (half a year)
//	paymethod 1 renewtime 1 -> 558.00   (one year)
var billingCycles = []struct {
	Key     string
	Label   string
	Months  int
	Paymath int
}{
	{"monthly", "月付", 1, 0},
	{"quarterly", "季付", 3, 2},
	{"semiannually", "半年付", 6, 3},
	{"annually", "年付", 12, 1},
}

func cycleOf(key string) (months, payMethod int, ok bool) {
	for _, cycle := range billingCycles {
		if cycle.Key == key {
			return cycle.Months, cycle.Paymath, true
		}
	}
	return 0, 0, false
}

// standardSpec is the reference configuration used for the catalogue "from"
// price and for the cycle list on the order page. It never becomes the price of
// a real order: every order is re-quoted through Quote with the chosen options.
const (
	standardOSDisk  = 30
	standardData    = 20
	standardFlux    = 1
	standardDisk    = "ebs"
	standardService = "基础服务"
)

type choice struct {
	ID   int
	Code string
	Name string
}

var prodTypeChoices = []choice{{1, "0", "通用型"}, {2, "1", "高频 CPU"}}
var diskTypeChoices = []choice{{1, "ebs", "高性能云盘"}, {2, "ssd", "SSD 超高速云盘"}}
var ccChoices = []choice{{1, "0", "基础版（默认）"}, {2, "3", "标准版"}, {3, "1", "硬防版"}}
var snapshotChoices = []choice{{1, "0", "不启用"}, {2, "1", "高级云快照（10 元/月）"}}
var serviceChoices = []choice{{1, "基础服务", "基础服务"}, {2, "铜牌服务", "铜牌服务"}, {3, "银牌服务", "银牌服务"}, {4, "金牌服务", "金牌服务"}}

func pick(list []choice, value string) (choice, bool) {
	for _, item := range list {
		if strconv.Itoa(item.ID) == value {
			return item, true
		}
	}
	return choice{}, false
}

func values(list []choice) []contracts.ConfigOptionValue {
	out := make([]contracts.ConfigOptionValue, 0, len(list))
	for _, item := range list {
		out = append(out, contracts.ConfigOptionValue{ID: item.ID, Name: item.Name})
	}
	return out
}

// catalogueName is what the administrator sees in the mapping list.
func catalogueName(room roomEntry) string {
	name := strings.TrimSpace(room.Name)
	if name == "" {
		name = "机房 " + text(room.RoomID)
	}
	return "西部数码弹性云 · " + name
}

// roomOf validates a catalogue product ID: one product per upstream machine
// room, which is the only stable identifier West publishes for a region.
func roomOf(id int, cfg *baseConfig) (roomEntry, bool) {
	for _, room := range cfg.Room {
		if int(dec(room.RoomID)) == id {
			return room, true
		}
	}
	return roomEntry{}, false
}

func standardQuery(room int) priceQuery {
	return priceQuery{
		CPU: 1, RAM: 1, OSDisk: standardOSDisk, DataDisk: standardData,
		Bandwidth: standardFlux, Room: room, DiskType: standardDisk, ServiceType: standardService,
		Units: 1, PayMethod: 0,
	}
}

// minDDoS is the cheapest protection value a high-protection room accepts; the
// live endpoint rejects ddos=0 with errcode 50003 on those rooms.
func minDDoS(room roomEntry) int {
	best := 0
	for _, value := range room.DDoS {
		if n := int(dec(value)); n > 0 && (best == 0 || n < best) {
			best = n
		}
	}
	return best
}

func (p *Provider) GetProductList() ([]contracts.UpstreamProduct, error) {
	ctx, cancel := p.withTimeout()
	defer cancel()
	cfg, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	products := make([]contracts.UpstreamProduct, 0, len(cfg.Room))
	var firstErr error
	for _, room := range cfg.Room {
		roomID := int(dec(room.RoomID))
		if roomID <= 0 {
			continue
		}
		query := standardQuery(roomID)
		// High-protection rooms only price with an explicit protection value.
		query.DDoS = minDDoS(room)
		cost, listPrice, err := p.createPrice(ctx, query)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if cost <= 0 || listPrice <= 0 {
			if firstErr == nil {
				firstErr = errors.New("西部数码未返回有效的原价或代理价")
			}
			continue
		}
		products = append(products, contracts.UpstreamProduct{
			ID:   roomID,
			Name: catalogueName(room),
			// The customer-facing price is the vendor list price; the agent cost
			// never leaves the provider and only feeds internal risk checks.
			Description:  strings.TrimSpace(room.Remark),
			Price:        listPrice,
			Cycle:        "monthly",
			PayType:      "prepaid",
			Type:         "cloud",
			CurrencyCode: "CNY",
			// West publishes no stock figure for elastic capacity.
			Stock: -1,
		})
	}
	if len(products) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, errors.New("西部数码未返回可售机房")
	}
	return products, nil
}

func (p *Provider) GetProductDetail(id int) (*contracts.UpstreamProductDetail, error) {
	ctx, cancel := p.withTimeout()
	defer cancel()
	cfg, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	room, ok := roomOf(id, cfg)
	if !ok {
		return nil, contracts.ErrProductUnavailable
	}
	query := standardQuery(id)
	query.DDoS = minDDoS(room)
	_, listPrice, err := p.createPrice(ctx, query)
	if err != nil {
		return nil, err
	}
	if listPrice <= 0 {
		return nil, errors.New("西部数码未返回有效原价")
	}
	options, err := p.configOptions(cfg, room)
	if err != nil {
		return nil, err
	}
	return &contracts.UpstreamProductDetail{
		UpstreamProduct: contracts.UpstreamProduct{
			ID: id, Name: catalogueName(room), Description: strings.TrimSpace(room.Remark),
			Price: listPrice, Cycle: "monthly", PayType: "prepaid", Type: "cloud",
			CurrencyCode: "CNY", Stock: -1,
		},
		Config:           map[string]string{"room": text(room.RoomID)},
		HasConfigOptions: true,
		ConfigOptions:    options,
	}, nil
}

// Config option identities are part of the wire contract with the platform: an
// order snapshot stores the chosen value under the option ID and only that ID
// reaches procurement, so every ID must decode back to exactly one field. They
// are intentionally stable across releases; never reuse or renumber them.
const (
	optProdType = 101
	optCPU      = 102
	optRAM      = 103
	optDiskType = 104
	optOSDisk   = 105
	optDataDisk = 106
	optFlux     = 107
	optCC       = 108
	optSnapshot = 109
	optService  = 110
	optDDoS     = 111
	optOS       = 112
)

// optionFields maps an option ID to the create parameter it feeds. The operating
// system is carried here rather than through OrderPage.Images because the
// delivery path does not forward ImageID to procurement. Its field must be
// "image": that is the field the order page treats as the image picker, and the
// generic configuration list skips it so the customer never sees two pickers.
//
// Resource fields use the names the shared order page already recognises
// (memory / system_disk / data_disk / bandwidth), so its existing slider control
// renders them instead of a long selector. IDs never change.
var optionFields = map[int]string{
	optProdType: "prodtype",
	optCPU:      "cpu",
	optRAM:      "memory",
	optDiskType: "disktype",
	optOSDisk:   "system_disk",
	optDataDisk: "data_disk",
	optFlux:     "bandwidth",
	optCC:       "cc",
	optSnapshot: "snapadv",
	optService:  "servicetype",
	optDDoS:     "ddos",
	optOS:       "image",
}

// configOptions is the provider-neutral ordering schema for one room. Prices are
// deliberately marked unknown: West prices the whole configuration, so only
// Quote may state an amount.
func (p *Provider) configOptions(cfg *baseConfig, room roomEntry) ([]contracts.ConfigOption, error) {
	unknown := false
	option := func(id int, field, name, kind string, required bool, list []choice, def string) contracts.ConfigOption {
		out := contracts.ConfigOption{ID: id, Name: name, Field: field, OptionType: kind, Required: required, Default: def}
		if len(list) > 0 {
			out.Options = values(list)
			for i := range out.Options {
				out.Options[i].PriceKnown = &unknown
			}
		}
		return out
	}
	cpuOptions := make([]contracts.ConfigOptionValue, 0, len(cfg.CPU))
	for _, cpu := range cfg.CPU {
		value := dec(cpu)
		if value <= 0 {
			continue
		}
		cpuOptions = append(cpuOptions, contracts.ConfigOptionValue{ID: int(value), Name: fmt.Sprintf("%g 核", value), PriceKnown: &unknown})
	}
	ramOptions := make([]contracts.ConfigOptionValue, 0, len(cfg.RAM))
	for _, ram := range cfg.RAM {
		value := dec(ram)
		if value <= 0 {
			continue
		}
		ramOptions = append(ramOptions, contracts.ConfigOptionValue{ID: int(value * 2), Name: fmt.Sprintf("%g G", value), PriceKnown: &unknown})
	}
	if len(cpuOptions) == 0 || len(ramOptions) == 0 {
		return nil, errors.New("西部数码未返回可用的 CPU 或内存规格")
	}
	qty := func(id int, field, name string, min, max, step float64, def string) contracts.ConfigOption {
		// Options must serialise as [] and never as null: the order page treats
		// every choice list as an array (option.options.find), so a null would
		// break the customer page for quantity options.
		return contracts.ConfigOption{ID: id, Name: name, Field: field, OptionType: "qty", Min: min, Max: max, Step: step, Default: def, Unit: "", Options: []contracts.ConfigOptionValue{}}
	}
	images := imagesOf(imageList(cfg))
	if len(images) == 0 {
		return nil, errors.New("西部数码未返回可用的操作系统")
	}
	options := []contracts.ConfigOption{
		option(optProdType, "prodtype", "CPU 类型", "select", true, prodTypeChoices, "1"),
		{ID: optCPU, Name: "CPU", Field: "cpu", OptionType: "select", Required: true, Default: "1", Options: cpuOptions},
		{ID: optRAM, Name: "内存", Field: "memory", OptionType: "select", Required: true, Default: "2", Options: ramOptions},
		option(optDiskType, "disktype", "云盘类型", "select", true, diskTypeChoices, "1"),
		qty(optOSDisk, "system_disk", "系统盘（G）", 30, 1000, 10, "30"),
		qty(optDataDisk, "data_disk", "数据盘（G）", 0, 1000, 10, "20"),
		qty(optFlux, "bandwidth", "带宽（M）", 1, 1000, 1, "1"),
		{ID: optOS, Name: "操作系统", Field: "image", OptionType: "select", Required: true, Default: "1", Options: images},
	}
	// High-protection rooms reject ddos=0 with errcode 50003, so the protection
	// value is a required choice drawn from what the vendor lists for the room.
	if list := ddosChoices(room); len(list) > 0 {
		options = append(options, option(optDDoS, "ddos", "DDoS 防护（G）", "select", true, list, strconv.Itoa(list[0].ID)))
	}
	// 服务标准不对外开放：West 的工单式服务等级由代理合同决定，客户无法自选。
	return append(options,
		option(optCC, "cc", "CC 防护", "select", false, ccChoices, "1"),
		option(optSnapshot, "snapadv", "高级云快照", "select", false, snapshotChoices, "1"),
	), nil
}

func ddosChoices(room roomEntry) []choice {
	out := make([]choice, 0, len(room.DDoS))
	for _, value := range room.DDoS {
		if n := int(dec(value)); n > 0 {
			out = append(out, choice{ID: n, Code: strconv.Itoa(n), Name: fmt.Sprintf("%dG", n)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// imageList gives the operating-system choices a stable integer identity while
// keeping the upstream value, which is what create and reinstall require.
func imageList(cfg *baseConfig) []osEntry {
	list := append([]osEntry(nil), cfg.OS...)
	sort.Slice(list, func(i, j int) bool { return list[i].Value < list[j].Value })
	return list
}

func imagesOf(list []osEntry) []contracts.ConfigOptionValue {
	out := make([]contracts.ConfigOptionValue, 0, len(list))
	for i, item := range list {
		out = append(out, contracts.ConfigOptionValue{ID: i + 1, Name: item.Name})
	}
	return out
}

// imageFamily names the picker card an operating system belongs to. The order
// page renders one card per family and lists that family's versions inside it,
// so a single flat list would show as one card holding everything.
func imageFamily(item osEntry) string {
	haystack := strings.ToLower(item.Name + " " + item.Value)
	switch {
	case strings.Contains(haystack, "宝塔"), strings.Contains(haystack, "建站"), strings.Contains(haystack, "slb"), strings.Contains(haystack, "面板"):
		return "预装面板"
	case strings.Contains(haystack, "win"):
		return "Windows"
	default:
		return "Linux"
	}
}

// imageGroupsOf keeps every image identity (1-based position in the sorted
// list) while grouping the picker cards by family.
func imageGroupsOf(list []osEntry) []contracts.ImageGroup {
	order := []string{"Windows", "Linux", "预装面板"}
	grouped := make(map[string]*contracts.ImageGroup, len(order))
	for i, item := range list {
		family := imageFamily(item)
		group, ok := grouped[family]
		if !ok {
			group = &contracts.ImageGroup{Name: family}
			grouped[family] = group
		}
		group.Child = append(group.Child, contracts.ConfigOptionValue{ID: i + 1, Name: item.Name})
	}
	out := make([]contracts.ImageGroup, 0, len(order))
	for _, name := range order {
		if group, ok := grouped[name]; ok && len(group.Child) > 0 {
			out = append(out, *group)
		}
	}
	return out
}

func (p *Provider) GetOrderPage(productID int) (*contracts.OrderPage, error) {
	ctx, cancel := p.withTimeout()
	defer cancel()
	cfg, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	room, ok := roomOf(productID, cfg)
	if !ok {
		return nil, contracts.ErrProductUnavailable
	}
	options, err := p.configOptions(cfg, room)
	if err != nil {
		return nil, err
	}
	cycles := make([]contracts.CycleItem, 0, len(billingCycles))
	for _, cycle := range billingCycles {
		query := standardQuery(productID)
		query.DDoS = minDDoS(room)
		// One period per quote; paymethod decides which period. The order page
		// shows the vendor list price (原价), never the agent cost.
		query.PayMethod = cycle.Paymath
		if _, listPrice, err := p.createPrice(ctx, query); err != nil {
			return nil, err
		} else if listPrice <= 0 {
			return nil, errors.New("西部数码未返回有效原价")
		} else {
			cycles = append(cycles, contracts.CycleItem{Billingcycle: cycle.Key, Label: cycle.Label, Price: listPrice})
		}
	}
	rooms := make([]contracts.ConfigOptionValue, 0, len(cfg.Room))
	for _, item := range cfg.Room {
		rooms = append(rooms, contracts.ConfigOptionValue{ID: int(dec(item.RoomID)), Name: item.Name})
	}
	list := imageList(cfg)
	return &contracts.OrderPage{
		Product:       contracts.UpstreamProduct{ID: productID, Name: catalogueName(room), Price: cycles[0].Price, Cycle: "monthly", PayType: "prepaid", Type: "cloud", CurrencyCode: "CNY", Stock: -1},
		DataCenters:   rooms,
		Images:        imagesOf(list),
		ImageGroups:   imageGroupsOf(list),
		ConfigOptions: options,
		Cycles:        cycles,
		// The interface can only open prepaid bandwidth-billed servers.
		NetworkTypes: []contracts.ConfigOptionValue{{ID: 1, Name: "按带宽计费"}},
	}, nil
}

// Quote prices one machine at the vendor list price (原价). The customer must
// never see the agent cost, so the cost is only read here to sanity-check that
// the list price really is the higher of the two; procurement keeps its own
// cost read for the balance check and the budget ceiling.
func (p *Provider) Quote(ctx context.Context, in contracts.QuoteRequest) (*contracts.QuoteResult, error) {
	_, payMethod, ok := cycleOf(in.Cycle)
	if !ok {
		return nil, errors.New("西部数码暂不支持该计费周期")
	}
	ctx, cancel := context.WithTimeout(ctx, requestBudget)
	defer cancel()
	cfg, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	room, ok := roomOf(in.ProductID, cfg)
	if !ok {
		return nil, contracts.ErrProductUnavailable
	}
	query, _, err := p.queryFromOptions(cfg, room, in.ConfigOptions)
	if err != nil {
		return nil, err
	}
	query.PayMethod = payMethod
	cost, listPrice, err := p.createPrice(ctx, query)
	if err != nil {
		return nil, err
	}
	if listPrice <= 0 || cost <= 0 {
		return nil, errors.New("西部数码未返回有效报价")
	}
	if listPrice < cost {
		// A list price below our own cost would mean a loss-making sale; refuse
		// instead of quietly selling at the lower number.
		return nil, errors.New("西部数码原价低于代理价，禁止按原价销售")
	}
	amount := strconv.FormatFloat(listPrice, 'f', 2, 64)
	return &contracts.QuoteResult{Total: amount, RenewalUnit: amount, SetupUnit: "0.00", Currency: "CNY"}, nil
}

// queryFromOptions translates an order snapshot (option ID -> value) into the
// documented create parameters. Unknown or out-of-range values are refused
// rather than silently replaced, because a wrong specification would still be
// charged. The selected operating system is returned separately: it is needed by
// create but never by a price query.
func (p *Provider) queryFromOptions(cfg *baseConfig, room roomEntry, options map[string]string) (priceQuery, string, error) {
	query := standardQuery(int(dec(room.RoomID)))
	query.ProdType = 0
	query.DiskType = standardDisk
	query.ServiceType = standardService
	query.CC = 0
	query.SnapshotAdv = 0
	osValue := ""
	for rawID, raw := range options {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(rawID))
		if err != nil {
			return query, "", errors.New("包含未知配置项")
		}
		field, ok := optionFields[id]
		if !ok {
			return query, "", errors.New("包含未知配置项")
		}
		switch field {
		case "prodtype":
			item, ok := pick(prodTypeChoices, value)
			if !ok {
				return query, "", errors.New("CPU 类型无效")
			}
			fmt.Sscanf(item.Code, "%d", &query.ProdType)
		case "cpu":
			cores, err := strconv.Atoi(value)
			if err != nil || !hasNumber(cfg.CPU, float64(cores)) {
				return query, "", errors.New("CPU 规格无效")
			}
			query.CPU = float64(cores)
		case "memory":
			half, err := strconv.Atoi(value)
			if err != nil || half <= 0 || !hasNumber(cfg.RAM, float64(half)/2) {
				return query, "", errors.New("内存规格无效")
			}
			query.RAM = float64(half) / 2
		case "disktype":
			item, ok := pick(diskTypeChoices, value)
			if !ok {
				return query, "", errors.New("云盘类型无效")
			}
			query.DiskType = item.Code
		case "servicetype":
			item, ok := pick(serviceChoices, value)
			if !ok {
				return query, "", errors.New("服务标准无效")
			}
			query.ServiceType = item.Code
		case "cc":
			item, ok := pick(ccChoices, value)
			if !ok {
				return query, "", errors.New("CC 防护选项无效")
			}
			fmt.Sscanf(item.Code, "%d", &query.CC)
		case "snapadv":
			item, ok := pick(snapshotChoices, value)
			if !ok {
				return query, "", errors.New("云快照选项无效")
			}
			fmt.Sscanf(item.Code, "%d", &query.SnapshotAdv)
		case "system_disk":
			n, err := wholeNumber(value, 30, 1000, 10)
			if err != nil {
				return query, "", errors.New("系统盘大小无效")
			}
			query.OSDisk = n
		case "data_disk":
			n, err := wholeNumber(value, 0, 1000, 10)
			if err != nil {
				return query, "", errors.New("数据盘大小无效")
			}
			query.DataDisk = n
		case "bandwidth":
			n, err := wholeNumber(value, 1, 1000, 1)
			if err != nil {
				return query, "", errors.New("带宽无效")
			}
			query.Bandwidth = n
		case "ddos":
			allowed, ok := pick(ddosChoices(room), value)
			if !ok {
				return query, "", errors.New("DDoS 防护值无效")
			}
			query.DDoS = allowed.ID
		case "image":
			osValue, err = p.osFromChoice(cfg, value)
			if err != nil {
				return query, "", err
			}
		default:
			return query, "", errors.New("包含未知配置项")
		}
	}
	if query.DDoS == 0 {
		query.DDoS = minDDoS(room)
	}
	return query, osValue, nil
}

// osFromChoice resolves the option value (a 1-based image identity) to the
// upstream operating system value that create and reinstall expect.
func (p *Provider) osFromChoice(cfg *baseConfig, value string) (string, error) {
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || id <= 0 {
		return "", errors.New("请选择操作系统")
	}
	list := imageList(cfg)
	if id > len(list) {
		return "", errors.New("所选操作系统不在西部数码当前列表中")
	}
	option := strings.TrimSpace(list[id-1].Value)
	if option == "" {
		return "", errors.New("所选操作系统缺少上游标识")
	}
	return option, nil
}

func hasNumber(list []json.Number, want float64) bool {
	for _, item := range list {
		if dec(item) == want {
			return true
		}
	}
	return false
}

func wholeNumber(raw string, min, max, step int) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max || (n-min)%step != 0 {
		return 0, errors.New("invalid")
	}
	return n, nil
}
