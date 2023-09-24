package w3s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	ipns_pb "github.com/ipfs/boxo/ipns/pb"
)

func (c *client) GetName(ctx context.Context, name string) (*ipns_pb.IpnsEntry, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/name/%s", "https://name.web3.storage", name), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.cfg.token))
	req.Header.Add("X-Client", clientName)
	res, err := c.cfg.hc.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode == 400 || res.StatusCode == 404 {
		return nil, err
	} else if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP response status: %s", res.Status)
	}

	d := json.NewDecoder(res.Body)
	var out struct {
		Value  string `json:"value"`
		Record string `json:"record"`
	}
	err = d.Decode(&out)
	if err != nil {
		return nil, err
	}
	ll, err := base64.StdEncoding.WithPadding(base64.StdPadding).DecodeString(out.Record)
	if err != nil {
		return nil, err
	}

	n := ipns_pb.IpnsEntry{}

	err = n.Unmarshal(ll)

	return &n, err
}

func (c *client) PutName(ctx context.Context, record *ipns_pb.IpnsEntry, name string) error {
	b, err := record.Marshal()
	if err != nil {
		return err
	}
	s := base64.StdEncoding.WithPadding(base64.StdPadding).EncodeToString(b)

	//w.Write(b)
	//w.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/name/%s", "https://name.web3.storage", name), strings.NewReader(s))
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.cfg.token))
	req.Header.Add("X-Client", clientName)

	res, err := c.cfg.hc.Do(req)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		//var b bytes.Buffer
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("HTTP response status: %s %s", res.Status, string(b))
	}
	d := json.NewDecoder(res.Body)
	var out struct {
		Value  string `json:"value"`
		Record string `json:"record"`
	}
	err = d.Decode(&out)
	if err != nil {
		return err
	}
	return err
}
