好，逐一展开。

---

## 一、JWT 验证核心流程

Teams 发来的每个 Webhook 请求，Header 里带一个 Bearer JWT，结构如下：

```
Header:  { "alg": "RS256", "kid": "abc123" }
Payload: { "iss": "https://api.botframework.com",
           "aud": "<你的 AppID>",
           "exp": 1234567890,
           "serviceurl": "https://smba.trafficmanager.net/..." }
```

验证流程分三步：拿公钥、验签名、验 claims。

### 1.1 JWKS 拉取与缓存

微软的公钥不是固定的，需要从 OpenID 元数据动态拉取，流程是两跳：

```
第一跳：GET https://login.botframework.com/v1/.well-known/openidconfiguration
        → 响应里有 jwks_uri 字段

第二跳：GET {jwks_uri}
        → 响应里有 keys[] 数组，每个 key 有 kid、n、e（RSA 公钥参数）
```

```go
type jwksCache struct {
    mu        sync.RWMutex
    keys      map[string]*rsa.PublicKey  // kid -> 公钥
    fetchedAt time.Time
    ttl       time.Duration              // 建议 24h
    httpClient *http.Client
}

func (c *jwksCache) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
    c.mu.RLock()
    key, ok := c.keys[kid]
    age := time.Since(c.fetchedAt)
    c.mu.RUnlock()

    if ok && age < c.ttl {
        return key, nil
    }

    // 缓存过期或 kid 不存在，重新拉取
    return c.refresh(ctx, kid)
}

func (c *jwksCache) refresh(ctx context.Context, kid string) (*rsa.PublicKey, error) {
    c.mu.Lock()
    defer c.mu.Unlock()

    // double-check，避免并发重复拉取
    if key, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < c.ttl {
        return key, nil
    }

    // 第一跳：拿 jwks_uri
    meta := struct {
        JwksURI string `json:"jwks_uri"`
    }{}
    if err := httpGetJSON(ctx, c.httpClient,
        "https://login.botframework.com/v1/.well-known/openidconfiguration",
        &meta); err != nil {
        return nil, fmt.Errorf("teams jwks meta: %w", err)
    }

    // 第二跳：拿 keys
    jwks := struct {
        Keys []struct {
            Kid string   `json:"kid"`
            N   string   `json:"n"`   // base64url 编码的 modulus
            E   string   `json:"e"`   // base64url 编码的 exponent
        } `json:"keys"`
    }{}
    if err := httpGetJSON(ctx, c.httpClient, meta.JwksURI, &jwks); err != nil {
        return nil, fmt.Errorf("teams jwks fetch: %w", err)
    }

    // 解析所有 key，存入缓存
    newKeys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
    for _, k := range jwks.Keys {
        pub, err := parseRSAPublicKey(k.N, k.E)
        if err != nil {
            continue // 跳过解析失败的 key，不阻断整体
        }
        newKeys[k.Kid] = pub
    }

    c.keys = newKeys
    c.fetchedAt = time.Now()

    key, ok := newKeys[kid]
    if !ok {
        // kid 在刷新后仍不存在，可能是伪造请求
        return nil, fmt.Errorf("teams jwks: unknown kid %q", kid)
    }
    return key, nil
}

// parseRSAPublicKey 从 base64url 编码的 n、e 还原 rsa.PublicKey
func parseRSAPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
    nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
    if err != nil {
        return nil, err
    }
    eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
    if err != nil {
        return nil, err
    }

    n := new(big.Int).SetBytes(nBytes)
    // e 通常是 65537，但要从字节正确解析
    var eInt int
    for _, b := range eBytes {
        eInt = eInt<<8 + int(b)
    }

    return &rsa.PublicKey{N: n, E: eInt}, nil
}
```

### 1.2 请求验证主函数

```go
func (c *jwksCache) validateRequest(ctx context.Context, r *http.Request, appID string) error {
    // 1. 取 Bearer token
    authHeader := r.Header.Get("Authorization")
    if !strings.HasPrefix(authHeader, "Bearer ") {
        return errors.New("missing bearer token")
    }
    tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

    // 2. 不验签先解析 header，取 kid
    //    jwt.ParseWithClaims 会验签，这里只是提前取 kid
    parts := strings.Split(tokenStr, ".")
    if len(parts) != 3 {
        return errors.New("malformed jwt")
    }
    headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
    if err != nil {
        return fmt.Errorf("jwt header decode: %w", err)
    }
    var header struct {
        Kid string `json:"kid"`
        Alg string `json:"alg"`
    }
    if err := json.Unmarshal(headerJSON, &header); err != nil {
        return err
    }
    if header.Alg != "RS256" {
        return fmt.Errorf("unexpected alg: %s", header.Alg)
    }

    // 3. 取对应公钥
    pubKey, err := c.getKey(ctx, header.Kid)
    if err != nil {
        return err
    }

    // 4. 用 golang-jwt/jwt 验签 + claims
    type botClaims struct {
        ServiceURL string `json:"serviceurl"`
        jwt.RegisteredClaims
    }

    token, err := jwt.ParseWithClaims(
        tokenStr,
        &botClaims{},
        func(t *jwt.Token) (any, error) {
            // 确认算法是 RS256，防止 alg:none 攻击
            if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
                return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
            }
            return pubKey, nil
        },
    )
    if err != nil {
        return fmt.Errorf("jwt verify: %w", err)
    }

    claims, ok := token.Claims.(*botClaims)
    if !ok || !token.Valid {
        return errors.New("jwt invalid")
    }

    // 5. 验证 claims
    //    aud 必须是自己的 AppID
    if !claims.VerifyAudience(appID, true) {
        return fmt.Errorf("jwt aud mismatch: got %v", claims.Audience)
    }
    //    iss 必须是 Bot Framework
    if claims.Issuer != "https://api.botframework.com" {
        return fmt.Errorf("jwt iss mismatch: %s", claims.Issuer)
    }
    //    serviceurl 建议与 Activity body 里的 serviceUrl 一致（可选加强校验）

    return nil
}
```

**关键安全点小结：**
- `alg` 必须强校验为 RS256，防止 `alg:none` 攻击
- `aud` 必须是你自己的 AppID，不能是通配
- JWKS 缓存 TTL 24h，但 kid 不命中时必须触发强制刷新（微软会 rotate key）
- `kid` 不命中且刷新后仍不存在 → 直接 reject，不做降级

---

## 二、Adaptive Card 格式

Teams 的 Adaptive Card 是一个 JSON 结构体，通过 Activity 的 `attachments` 字段发送。以下是几个最常用的 Card 模板。

### 2.1 基础消息 Card（富文本替代）

```go
// SimpleCard 当普通 markdown 渲染效果不佳时用 Card 替代
func BuildSimpleCard(title, body string) json.RawMessage {
    card := map[string]any{
        "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
        "type":    "AdaptiveCard",
        "version": "1.5",
        "body": []any{
            map[string]any{
                "type":   "TextBlock",
                "text":   title,
                "weight": "Bolder",
                "size":   "Medium",
                "wrap":   true,
            },
            map[string]any{
                "type": "TextBlock",
                "text": body,
                "wrap": true,
            },
        },
    }
    raw, _ := json.Marshal(card)
    return raw
}
```

### 2.2 Poll Card（投票）

OpenClaw 的 poll 用 Adaptive Card 实现，因为 Teams 没有原生 poll API：

```go
type PollOption struct {
    Label string
    Value string  // 提交时回传的值
}

func BuildPollCard(question string, options []PollOption) json.RawMessage {
    // 构造 ChoiceSet（单选）
    choices := make([]any, len(options))
    for i, opt := range options {
        choices[i] = map[string]any{
            "title": opt.Label,
            "value": opt.Value,
        }
    }

    card := map[string]any{
        "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
        "type":    "AdaptiveCard",
        "version": "1.5",
        "body": []any{
            map[string]any{
                "type":   "TextBlock",
                "text":   question,
                "weight": "Bolder",
                "size":   "Medium",
                "wrap":   true,
            },
            map[string]any{
                "type":  "Input.ChoiceSet",
                "id":    "pollChoice",
                "style": "expanded",   // expanded = radio button，compact = dropdown
                "choices": choices,
            },
        },
        "actions": []any{
            map[string]any{
                "type":  "Action.Submit",
                "title": "Submit",
                "data": map[string]any{
                    "actionType": "pollVote",  // handler 里用这个字段识别 invoke 类型
                },
            },
        },
    }
    raw, _ := json.Marshal(card)
    return raw
}
```

用户点击 Submit 后，Teams 会发一个 `invoke` 类型的 Activity，body 里包含：

```json
{
  "type": "invoke",
  "name": "adaptiveCard/action",
  "value": {
    "actionType": "pollVote",
    "pollChoice": "option_a"
  }
}
```

在 `webhook.go` 的 `handleInvokeActivity` 里处理这个回调即可。

### 2.3 发送 Card 的 Activity 结构

```go
// 发送 Card 时，Text 留空，内容全在 Attachments
func (s *TeamsSender) sendCard(ctx context.Context, serviceURL, conversationID, replyToID string, card json.RawMessage) error {
    activity := map[string]any{
        "type":      "message",
        "replyToId": replyToID,
        // Text 故意留空，Card 和 Text 不要同时出现，Teams 渲染会错乱
        "attachments": []any{
            map[string]any{
                "contentType": "application/vnd.microsoft.card.adaptive",
                "content":     json.RawMessage(card),
                // 注意：content 是嵌套对象，不是 string，不要 json.Marshal 两次
            },
        },
    }

    url := fmt.Sprintf("%s/v3/conversations/%s/activities/%s",
        serviceURL, conversationID, replyToID)
    return s.doPost(ctx, url, activity)
}
```

**一个常见坑：** `content` 字段必须是 JSON 对象，不能是 JSON 字符串。如果你先 `json.Marshal(card)` 得到 `[]byte`，再放进外层结构体，外层 Marshal 时会对它做二次转义变成字符串，Teams 就无法解析。解决办法是用 `json.RawMessage` 类型，它在 Marshal 时会原样嵌入，不做转义。

---

## 三、Graph API 分页处理

Graph API 采用 OData 分页，每次返回一页数据，下一页的地址放在响应的 `@odata.nextLink` 字段里。

### 3.1 通用分页迭代器

```go
// graphPage 是一个泛型分页迭代器
// T 是单条记录的类型，比如 GraphMessage、GraphMember
type graphPage[T any] struct {
    Values   []T    `json:"value"`
    NextLink string `json:"@odata.nextLink"` // 空字符串表示没有下一页
}

// fetchAllPages 自动翻页，直到取完或达到 maxItems 限制
func fetchAllPages[T any](
    ctx context.Context,
    client *GraphClient,
    firstURL string,
    maxItems int,
) ([]T, error) {
    var all []T
    url := firstURL

    for url != "" {
        if maxItems > 0 && len(all) >= maxItems {
            break
        }

        var page graphPage[T]
        if err := client.getJSON(ctx, url, &page); err != nil {
            return all, fmt.Errorf("graph page fetch: %w", err)
        }

        all = append(all, page.Values...)
        url = page.NextLink

        // 每次翻页加一点 sleep，避免触发 Graph API 限速（429）
        // 生产环境建议做 retry-after header 处理
        if url != "" {
            time.Sleep(100 * time.Millisecond)
        }
    }

    if maxItems > 0 && len(all) > maxItems {
        all = all[:maxItems]
    }
    return all, nil
}
```

### 3.2 历史消息拉取（带分页）

```go
type GraphMessage struct {
    ID              string    `json:"id"`
    CreatedDateTime time.Time `json:"createdDateTime"`
    From            struct {
        User *struct {
            DisplayName string `json:"displayName"`
            ID          string `json:"id"`
        } `json:"user"`
    } `json:"from"`
    Body struct {
        ContentType string `json:"contentType"` // "text" | "html"
        Content     string `json:"content"`
    } `json:"body"`
    Attachments []struct {
        ContentType string `json:"contentType"`
        ContentURL  string `json:"contentUrl"`
        Name        string `json:"name"`
    } `json:"attachments"`
    // 回复链
    ReplyToID string `json:"replyToId"`
}

func (g *GraphClient) GetChannelHistory(
    ctx context.Context,
    teamID, channelID string,
    limit int,             // 对应 historyLimit 配置
) ([]GraphMessage, error) {
    // $top 控制每页条数，最大 50（Graph API 限制）
    // $orderby 按时间倒序，取最新的
    pageSize := min(limit, 50)
    url := fmt.Sprintf(
        "%s/teams/%s/channels/%s/messages?$top=%d&$orderby=createdDateTime desc",
        g.baseURL, teamID, channelID, pageSize,
    )

    msgs, err := fetchAllPages[GraphMessage](ctx, g, url, limit)
    if err != nil {
        return nil, err
    }

    // Graph 返回的是倒序，反转为正序（旧 → 新）供 agent 用作 context
    slices.Reverse(msgs)
    return msgs, nil
}
```

### 3.3 限速处理（429 Retry-After）

Graph API 有 throttling，超限时返回 429，Header 里有 `Retry-After` 秒数：

```go
func (g *GraphClient) getJSON(ctx context.Context, url string, out any) error {
    const maxRetries = 3

    for attempt := range maxRetries {
        req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
        token, err := g.tokenProvider.GetToken(ctx)
        if err != nil {
            return err
        }
        req.Header.Set("Authorization", "Bearer "+token)
        req.Header.Set("Accept", "application/json")

        resp, err := g.httpClient.Do(req)
        if err != nil {
            return err
        }

        if resp.StatusCode == http.StatusTooManyRequests {
            resp.Body.Close()
            // 读取 Retry-After，等待后重试
            retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), 5*time.Second)
            slog.Warn("graph api throttled",
                "url", url,
                "retry_after", retryAfter,
                "attempt", attempt+1,
            )
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(retryAfter):
                continue
            }
        }

        defer resp.Body.Close()
        if resp.StatusCode >= 300 {
            body, _ := io.ReadAll(resp.Body)
            return fmt.Errorf("graph %d: %s", resp.StatusCode, string(body))
        }

        return json.NewDecoder(resp.Body).Decode(out)
    }
    return fmt.Errorf("graph: max retries exceeded for %s", url)
}

func parseRetryAfter(header string, fallback time.Duration) time.Duration {
    if header == "" {
        return fallback
    }
    secs, err := strconv.Atoi(header)
    if err != nil {
        return fallback
    }
    return time.Duration(secs) * time.Second
}
```

### 3.4 成员列表分页（用于 allowFrom ID 解析）

```go
type GraphMember struct {
    ID          string `json:"id"`           // AAD Object ID
    DisplayName string `json:"displayName"`
    Mail        string `json:"mail"`         // UPN / email
}

func (g *GraphClient) GetTeamMembers(ctx context.Context, teamID string) ([]GraphMember, error) {
    url := fmt.Sprintf("%s/groups/%s/members?$select=id,displayName,mail&$top=100",
        g.baseURL, teamID)
    // 大型组织的 team 可能有上千成员，需要完整翻页
    return fetchAllPages[GraphMember](ctx, g, url, 0) // 0 = 不设上限
}
```

这个函数主要用于启动时把 `allowFrom` 配置里的 `user@org.com` 格式解析成稳定的 AAD Object ID，解析结果缓存在内存里，不需要每条消息都查一次。

---

三块核心细节都在这了。整体依赖关系是：JWT 验证是上行安全基础，必须最先做；Adaptive Card 是发送侧的格式扩展，P1 阶段加；Graph API 分页是历史消息和成员解析的共用基础设施，用泛型迭代器统一封装后，各个业务函数直接复用就行。