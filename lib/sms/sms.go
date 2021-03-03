package sms

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os"
)

type SMS struct {
	key      string
	senderID string
}

type Message struct {
	To      string `json:"to"`
	From    string `json:"from"`
	Sms     string `json:"sms"`
	Type    string `json:"type"`
	Channel string `json:"channel"`
	ApiKey  string `json:"api_key"`
}

type Response struct {
	MessageId string  `json:"message_id"`
	Message   string  `json:"message"`
	Balance   float64 `json:"balance"`
	User      string  `json:"user"`
}

const API_ENDPOINT = "https://termii.com/api/sms/send"

func New(apiKey string) *SMS {
	if apiKey == "" {
		log.Fatal("termii api key required")
	}
	return &SMS{apiKey, os.Getenv("TERMII_SENDER_ID")}
}

func (s *SMS) SendTextMessage(msg Message) (*Response, error) {
	msg.ApiKey = s.key
	msg.Type = "plain"
	msg.Channel = "generic"
	msg.From = s.senderID

	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", API_ENDPOINT, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	payload, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return nil, err
	}

	var response Response

	err = json.Unmarshal(payload, &response)

	if err != nil {
		return nil, err
	}

	return &response, nil
}
