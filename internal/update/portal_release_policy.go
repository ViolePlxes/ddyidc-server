package update

import (
 "bytes"
 "encoding/json"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "regexp"
 "strings"
)

// Only these two public build manifests are registered. This never permits
// arbitrary portal JSON, business data, paths outside assets, or missing files.
func validatePortalReleaseAssets(root string) error {
 if _,err:=os.Stat(filepath.Join(root,"portal"));os.IsNotExist(err) {return nil}
 decode:=func(name string,target any) error {
  raw,err:=os.ReadFile(filepath.Join(root,"portal",name));if err!=nil{return err}
  if len(raw)>65536{return fmt.Errorf("oversized portal manifest: %s",name)}
  decoder:=json.NewDecoder(bytes.NewReader(raw));decoder.DisallowUnknownFields()
  if err=decoder.Decode(target);err!=nil{return err};if decoder.Decode(new(any))!=io.EOF{return fmt.Errorf("invalid portal manifest: %s",name)};return nil
 }
 var themes []struct{ID string `json:"id"`;Label string `json:"label"`;Version int `json:"version"`;SchemaVersion int `json:"schemaVersion"`}
 if err:=decode("themes.json",&themes);err!=nil{return err}
 if len(themes)!=1||themes[0].ID!="cloudflare-reference"||themes[0].Version!=1||themes[0].SchemaVersion!=1||strings.TrimSpace(themes[0].Label)==""||len(themes[0].Label)>256{return fmt.Errorf("invalid portal theme registry")}
 var manifest struct{Theme string `json:"theme"`;Version int `json:"version"`;Styles []string `json:"styles"`;Script string `json:"script"`}
 if err:=decode("cloudflare-reference.json",&manifest);err!=nil{return err}
 if manifest.Theme!="cloudflare-reference"||manifest.Version!=1||len(manifest.Styles)<1||len(manifest.Styles)>100{return fmt.Errorf("invalid portal asset manifest")}
 files:=append([]string{},manifest.Styles...)
 for _,path:=range manifest.Styles {if !regexp.MustCompile(`^/assets/[A-Za-z0-9_.-]+\.css$`).MatchString(path)||strings.Contains(path,".."){return fmt.Errorf("unsafe portal stylesheet")}}
 if !regexp.MustCompile(`^/assets/portal-content-[a-f0-9]{16}\.js$`).MatchString(manifest.Script){return fmt.Errorf("unsafe portal runtime")}
 files=append(files,manifest.Script,"/portal/cloudflare-reference.html")
 for _,path:=range files {info,err:=os.Lstat(filepath.Join(root,filepath.FromSlash(path[1:])));if err!=nil{return err};if !info.Mode().IsRegular(){return fmt.Errorf("portal asset is not a regular file")}}
 return nil
}
