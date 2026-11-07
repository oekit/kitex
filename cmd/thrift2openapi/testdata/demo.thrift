namespace go demo

include "common.thrift"

// 通知发送请求。
// 刻意覆盖手搓 regex 解析器的破损面：多行字段、默认值、嵌套注解、跨文件引用。
struct SendReq {
    1: required string title (api.body = "title") // 标题，展示在通知中心
    2: required string content (api.json = "content") // 正文，Markdown
    3: optional list<string> channels = [
        "email"
    ] // 投递渠道，缺省仅 email
    4: optional common.Page page // 分页（include 引用）
    5: optional string trace (api.header = "X-Request-ID") // 链路头
}

struct SendResp {
    1: required i64 notice_id // 通知 ID
    2: required common.Status status // 状态
    3: optional map<string, string> extra // 扩展信息
    4: optional common.Timestamp sent_at // 发送时间
}

// 用户
struct User {
    1: required i64 uid // 用户 ID
    2: required string name // 昵称
    3: optional User manager // 上级（自引用 $ref）
    4: optional set<string> tags // 标签（set → uniqueItems）
}

struct NoticeIDReq {
    1: required i64 id (api.path = "id") // 通知 ID
}

struct UserQueryReq {
    1: required i64 uid (api.query = "uid") // 用户 ID
    2: optional string keyword (api.query = "q") // 搜索词
}

struct UpdateUserReq {
    1: required i64 uid (api.body = "uid") // 用户 ID
    2: optional string name (api.body = "name") // 新昵称
}

struct UploadReq {
    1: required string filename (api.form = "filename") // 文件名
    2: required binary content (api.file = "content") // 文件内容
}

service DemoService {
    // 发送通知
    // 幂等：携带 Idempotency-Key 头
    SendResp sendNotice(1: SendReq req) (api.post = "/api/v1/send")

    // 通知详情
    SendResp getNotice(1: NoticeIDReq req) (api.get = "/api/v1/notice/:id")

    // 用户查询（query 绑定示例）
    User getUser(1: UserQueryReq req) (api.get = "/api/v1/user")

    // 更新用户
    void updateUser(1: UpdateUserReq req) (api.put = "/api/v1/user")

    // 删除通知
    void deleteNotice(1: NoticeIDReq req) (api.delete = "/api/v1/notice/:id")

    // 上传头像（multipart 示例）
    void uploadAvatar(1: UploadReq req) (api.post = "/api/v1/avatar")

    // 内部接口：路由存在但不进文档（api.skip 豁免注解）
    void internalOp(1: NoticeIDReq req) (api.post = "/api/v1/internal", api.skip = "true")

    // 健康检查：代码注册（非 IDL 路由注册路径的对照示例），
    // 生成时由 -exempt /healthz 显式豁免（替代手搓脚本的硬编码豁免表）
    void healthz() (api.get = "/healthz")

    // 登录：免 JWT 标记（api.noauth）
    void login(1: UserQueryReq req) (api.post = "/api/v1/login", api.noauth = "true")

    // 纯 RPC 方法：无 api.<verb> 注解，不进 HTTP 文档
    SendResp pollNotice(1: NoticeIDReq req)
}
