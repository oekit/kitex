namespace go common

// 通知状态（线上按 i32 传输）
enum Status {
    STATUS_UNKNOWN = 0 // 未知
    STATUS_ACTIVE = 1 // 活跃
    STATUS_BANNED = 2 // 封禁
}

// 毫秒时间戳
typedef i64 Timestamp

// 分页参数（跨 IDL include 复用）
struct Page {
    1: required i32 page = 1 (api.query = "page") // 页码，从 1 开始
    2: required i32 page_size = 20 (api.query = "page_size") // 每页条数
}
