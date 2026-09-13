package adminread

import "testing"

func TestValidateCustomerProfile(t *testing.T) {
	base := updateCustomerInput{createCustomerInput: createCustomerInput{Name: "测试用户", Email: "person@example.com"}, Language: "zh-CN"}
	if _, msg := validateProfile(base); msg != "" {
		t.Fatal(msg)
	}
	cases := []struct {
		name string
		edit func(*updateCustomerInput)
	}{
		{"short password", func(v *updateCustomerInput) { v.Password = "123" }},
		{"invalid phone", func(v *updateCustomerInput) { v.CountryCode = "+1"; v.Mobile = "abc" }},
		{"invalid language", func(v *updateCustomerInput) { v.Language = "invalid" }},
		{"long remark", func(v *updateCustomerInput) {
			for i := 0; i < 1001; i++ {
				v.Remark += "字"
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := base
			c.edit(&v)
			if _, msg := validateProfile(v); msg == "" {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}
