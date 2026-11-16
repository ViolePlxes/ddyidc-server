package west

import (
	"encoding/json"
	"testing"

	"ddyidc/internal/upstream/provider/contracts"
)

// The live trial machine answered status=-1 while it was being created, a code
// the vendor table does not list. It must not surface as an unknown state.
func TestPowerStateCoversCreationInProgress(t *testing.T) {
	for code, want := range map[string]string{
		"-1": "provisioning",
		"0":  "running",
		"1":  "stopped",
		"2":  "stopped",
		"3":  "suspended",
		"9":  "",
	} {
		state := &runState{State: json.Number(code)}
		if got := powerOf(state); got != want {
			t.Fatalf("status %s => %q, want %q", code, got, want)
		}
	}
}

func TestMemoryIsShownInGigabytes(t *testing.T) {
	for raw, want := range map[string]string{
		"1024": "1 G",
		"512":  "0.5 G",
		"1536": "1.5 G",
		"4096": "4 G",
		"":     "",
		"abc":  "abc",
	} {
		if got := formatMemory(raw); got != want {
			t.Fatalf("memory %q => %q, want %q", raw, got, want)
		}
	}
}

func TestOperatingSystemPrefersVendorDisplayName(t *testing.T) {
	if got := firstText("CentOS7 纯净版", "linux_centos7_x64_clean"); got != "CentOS7 纯净版" {
		t.Fatalf("display name not preferred: %q", got)
	}
	if got := firstText("", "linux_centos7_x64_clean"); got != "linux_centos7_x64_clean" {
		t.Fatalf("fallback lost: %q", got)
	}
	if got := firstText("  ", ""); got != "" {
		t.Fatalf("blank values should collapse: %q", got)
	}
}

// The order page renders the operating system from the config option whose field
// is "image", using orderPage.imageGroups as the choices. Their identities must
// line up or the picker shows one set and procurement receives another.
func TestImageOptionAndImageGroupsShareIdentities(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBody("60.00"),
	})
	page, err := p.GetOrderPage(31)
	if err != nil {
		t.Fatal(err)
	}
	var image *contracts.ConfigOption
	for i := range page.ConfigOptions {
		if page.ConfigOptions[i].Field == "image" {
			image = &page.ConfigOptions[i]
		}
		if page.ConfigOptions[i].Field == "os" {
			t.Fatal("legacy os field would render a second picker")
		}
	}
	if image == nil || image.ID != optOS || !image.Required {
		t.Fatalf("image option missing or wrong: %+v", image)
	}
	if len(page.ImageGroups) == 0 || len(page.ImageGroups[0].Child) == 0 {
		t.Fatal("image groups missing")
	}
	optionIDs := map[int]bool{}
	for _, choice := range image.Options {
		optionIDs[choice.ID] = true
	}
	for _, choice := range page.ImageGroups[0].Child {
		if !optionIDs[choice.ID] {
			t.Fatalf("image group id %d is not a selectable option value", choice.ID)
		}
	}
	if len(optionIDs) != len(page.ImageGroups[0].Child) {
		t.Fatal("image option and image groups list different counts")
	}
}
