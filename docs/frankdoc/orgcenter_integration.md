# DingTalk User ID Adaptation (Person Code)

This plan outlines how to adapt the DingTalk channel to use a `person_code` from an external "Organization Center" as the primary user identifier, instead of the default DingTalk `senderStaffId`.

## Background

Current DingTalk inbound processing uses `senderStaffId` as the `userID`. The user wants to map this to a `person_code` which is retrieved from an external Organization Center using the user's mobile phone number.

## Proposed Changes

### 1. DingTalk Client Enhancement

Add a method to `DingtalkClient` to fetch a user's mobile phone number using their `staffId`.

#### [MODIFY] [client.go](file:///opt/projects/github/goclaw/internal/channels/dingtalk/client.go)

- Add `GetUserInfo(ctx context.Context, staffID string) (map[string]any, error)`
- This will call `https://api.dingtalk.com/v1.0/contact/users/{userId}`.

### 2. Organization Center Integration

Create a new client to handle the `mobile` -> `person_code` lookup.

#### [NEW] [org_center.go](file:///opt/projects/github/goclaw/internal/channels/dingtalk/org_center.go)

- Implement `OrgCenterClient` with a `LookupPersonCode(ctx context.Context, mobile string) (string, error)` method.
- This client should be configurable (endpoint, token).

### 3. Configuration Update

Add settings for the Organization Center to the DingTalk channel configuration.

#### [MODIFY] [config_channels.go](file:///opt/projects/github/goclaw/internal/config/config_channels.go)

- Add `OrgCenter` struct to `DingtalkConfig`:
  ```go
  type OrgCenterConfig struct {
      Enabled  bool   `json:"enabled"`
      Endpoint string `json:"endpoint"`
      Token    string `json:"token"`
  }
  ```

### 4. Messaging Logic Adaptation

Update `processInbound` to perform the ID translation before calling `HandleMessage`.

#### [MODIFY] [messaging.go](file:///opt/projects/github/goclaw/internal/channels/dingtalk/messaging.go)

- Introduce an LRU cache in `DingtalkChannel` for `staffId` -> `person_code`.
- In `processInbound`:
  1. Extract `staffId`.
  2. If `OrgCenter` is enabled:
     - Check cache for `person_code`.
     - If miss:
       a. Call `client.GetUserInfo` to get `mobile`.
       b. Call `OrgCenterClient.LookupPersonCode` with `mobile`.
       c. Update cache.
     - Set `userID = person_code`.
  3. Proceed with `c.HandleMessage(userID, ...)`.

## Open Questions

- **Organization Center API**: What is the exact API endpoint and request/response format for the Organization Center?
- **Fallback Policy**: If the mobile lookup or the person_code lookup fails, should we:
  1. Fall back to the original `senderStaffId`?
  2. Reject the message with an error?
  3. Use a placeholder ID?
- **Permissions**: Has the DingTalk bot been granted the "Contact personal info read permission" (通讯录个人信息读权限) in the DingTalk console? This is required to get the mobile number.

## Verification Plan

### Automated Tests

- Mock DingTalk API and Organization Center API.
- Test `processInbound` with various scenarios (cache hit, cache miss, API failure).

### Manual Verification

- Deploy to a test environment.
- Send a message from DingTalk and verify that the logs/database show the `person_code` as the `actor_id`.

# 组织中心的接口信息

## 建议

是否可以定一个 Mock 的接口?

因为我们组织中心在内网现在并不能够及时地去集成，但是在测试阶段，我希望可以测试过去。比如可以在 Mock 中返回一个 personCode，让代码继续验证通路；同时在独立的接口中去验证组织中心的接口，可以返回 personCode 的接口。

这样做可以实现功能的测试以及和接口通路的隔离

## 组织中心现有接口协议说明

调用方式：

```shell
curl -X GET --location 'https://orgcenter.capitaleco-pro.com/api/OrgPersonRestApi/listUsers?phone=13693610685' \
--header 'Content-Type: application/json' \
--header 'Access-Key: 7z87iM1q3A03rR1W' \
--header 'Secret-Key: zNun20NW8elk76XoLG6BVPACeHGlZrxa' \
--data ''
```

返回值如下：

```json
{
  "status": 200,
  "success": true,
  "message": "调用成功!",
  "data": [
    {
      "id": "1823172923439828993",
      "personId": null,
      "tenantCode": null,
      "personName": "韩昭芳",
      "personCode": "161533",
      "personCategory": "1",
      "startTime": null,
      "endTime": null,
      "email": "hanzf@capitalwater.cn",
      "telPhone": null,
      "phonenumber": "13693610685",
      "sex": "01001",
      "personStatus": "1",
      "dataSrcSystem": null,
      "sourcesType": "2",
      "nation": null,
      "nationality": null,
      "politicalOutlook": null,
      "marital": null,
      "education": null,
      "school": null,
      "speciality": null,
      "jobTitle": null,
      "seq": null,
      "timeInWork": null,
      "depositBank": null,
      "bankAccount": null,
      "createBy": "wangzhe1",
      "createTime": 1723513272000,
      "updateBy": null,
      "updateTime": null,
      "remark": null,
      "codeId": null,
      "nodeCode": null,
      "parentDeptCode": null,
      "signaturePath": null,
      "updateImg": null,
      "orgStructureCode": null,
      "hrUpdateSign": null,
      "opType": null,
      "isVirtual": null,
      "IDCard": "370883198609014854",
      "HRcode": null
    }
  ],
  "host": "127.0.0.1",
  "timestamp": "2026-04-26 02:12:48"
}
```
