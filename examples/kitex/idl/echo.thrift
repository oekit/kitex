namespace go kitex.echo

struct EchoRequest {
    1: string message
}

struct EchoResponse {
    1: string message
    2: i64 timestamp
}

service EchoService {
    EchoResponse Echo(1: EchoRequest req)
}
