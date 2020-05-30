package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/danopia/plex-backhaul/common"
)

var httpClient *http.Client = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 10,
	},
	Timeout: 15 * time.Second,
}

func (pc *ProxyClient) SubmitWireRequest(wireReq *common.InnerReq) (*common.InnerResp, error) {
	jsonValue, err := json.Marshal(wireReq)
	if err != nil {
		return nil, err
	}
	// log.Println(string(jsonValue))

	submitReq, err := http.NewRequest("POST", pc.BaseURL+"/backhaul/submit", bytes.NewBuffer(jsonValue))
	if err != nil {
		return nil, err
	}
	submitReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(submitReq)
	if err != nil {
		return nil, err
	}

	var wireResp common.InnerResp
	err = json.NewDecoder(resp.Body).Decode(&wireResp)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	return &wireResp, nil
}

func (pc *ProxyClient) CancelRequest(requestId string) error {
	jsonValue, err := json.Marshal(requestId)
	if err != nil {
		return err
	}
	log.Println("Attempting to cancel req", string(jsonValue))

	cancelReq, err := http.NewRequest("POST", pc.BaseURL+"/backhaul/cancel", bytes.NewBuffer(jsonValue))
	if err != nil {
		return err
	}
	cancelReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(cancelReq)
	if err != nil {
		return err
	}

	io.Copy(ioutil.Discard, resp.Body)
	resp.Body.Close()

	return nil
}
