package provider
import ("bytes"; "crypto/aes"; "crypto/cipher"; "crypto/rand"; "crypto/sha256"; "encoding/base64"; "fmt"; "io"; "net/http"; "os"; "time")
// Base provides bounded HTTP, AES-GCM secret storage, and request signatures to adapters.
type Base struct { Client *http.Client }
func NewBase() Base { return Base{Client:&http.Client{Timeout:30*time.Second}} }
func key() []byte { sum:=sha256.Sum256([]byte(os.Getenv("UPSTREAM_AES_KEY"))); return sum[:] }
func Encrypt(value string) (string,error) { block,e:=aes.NewCipher(key()); if e!=nil{return "",e}; g,e:=cipher.NewGCM(block); if e!=nil{return "",e}; nonce:=make([]byte,g.NonceSize()); if _,e=io.ReadFull(rand.Reader,nonce);e!=nil{return "",e}; return base64.StdEncoding.EncodeToString(g.Seal(nonce,nonce,[]byte(value),nil)),nil }
func Decrypt(value string) (string,error) { raw,e:=base64.StdEncoding.DecodeString(value); if e!=nil{return "",e}; block,e:=aes.NewCipher(key());if e!=nil{return "",e}; g,e:=cipher.NewGCM(block);if e!=nil{return "",e}; if len(raw)<g.NonceSize(){return "",fmt.Errorf("invalid encrypted value")}; v,e:=g.Open(nil,raw[:g.NonceSize()],raw[g.NonceSize():],nil);return string(v),e }
func (b Base) Do(req *http.Request) (*http.Response,error) { return b.Client.Do(req) }
func (b Base) JSON(method,url string,body []byte) (*http.Request,error) { return http.NewRequest(method,url,bytes.NewReader(body)) }
