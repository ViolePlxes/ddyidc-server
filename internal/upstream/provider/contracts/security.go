package contracts

type SecurityGroup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Applied     bool   `json:"applied"`
}
type SecurityRule struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	Port      string `json:"port"`
	IP        string `json:"ip"`
}
type SecurityProvider interface {
	SecurityGroups(int64) ([]SecurityGroup, error)
	SecurityCommand(int64, string, map[string]string) error
	SecurityRules(int64, string) ([]SecurityRule, error)
}
