package jiraapi

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Sprint 是 Jira Sprint。日期是 Jira 返回的原样字符串（ISO 格式）。
type Sprint struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"` // 大写：ACTIVE / CLOSED / FUTURE
	Start    string `json:"start"`
	End      string `json:"end"`
	Complete string `json:"complete"`
}

// ParseSprints 解析 Sprint 自定义字段。新版返回对象数组；旧版 server 返回
// com.atlassian.greenhopper.service.sprint.Sprint@…[id=1,rapidViewId=2,state=ACTIVE,name=Sprint 1,startDate=…,…] 字符串数组。
func ParseSprints(raw json.RawMessage) []Sprint {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var out []Sprint
	for _, item := range items {
		var obj struct {
			ID           json.Number `json:"id"`
			Name         string      `json:"name"`
			State        string      `json:"state"`
			StartDate    string      `json:"startDate"`
			EndDate      string      `json:"endDate"`
			CompleteDate string      `json:"completeDate"`
		}
		if err := json.Unmarshal(item, &obj); err == nil && (obj.ID != "" || obj.Name != "") {
			out = append(out, Sprint{ID: obj.ID.String(), Name: obj.Name, State: strings.ToUpper(obj.State),
				Start: obj.StartDate, End: obj.EndDate, Complete: obj.CompleteDate})
			continue
		}
		var text string
		if err := json.Unmarshal(item, &text); err == nil {
			if s, ok := parseLegacySprint(text); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

var legacyKey = regexp.MustCompile(`(?:^|,)(id|rapidViewId|state|name|startDate|endDate|completeDate|sequence|goal|autoStartStop|synced|originBoardId)=`)

func parseLegacySprint(text string) (Sprint, bool) {
	open := strings.Index(text, "[")
	closing := strings.LastIndex(text, "]")
	if open < 0 || closing <= open {
		return Sprint{}, false
	}
	body := text[open+1 : closing]
	matches := legacyKey.FindAllStringSubmatchIndex(body, -1)
	values := map[string]string{}
	for i, m := range matches {
		end := len(body)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		value := body[m[1]:end]
		if value == "<null>" {
			value = ""
		}
		values[body[m[2]:m[3]]] = value
	}
	if _, err := strconv.Atoi(values["id"]); err != nil {
		return Sprint{}, false
	}
	return Sprint{ID: values["id"], Name: values["name"], State: strings.ToUpper(values["state"]),
		Start: values["startDate"], End: values["endDate"], Complete: values["completeDate"]}, true
}
