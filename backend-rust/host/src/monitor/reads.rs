use super::*;
use http_body_util::combinators::BoxBody;
type Body = BoxBody<Bytes, hyper::Error>;
type AdapterSender = hyper::client::conn::http1::SendRequest<Full<Bytes>>;

impl Monitor {
    pub(super) async fn respond(
        &self,
        req: Request<Incoming>,
        adapter: &Mutex<Option<AdapterSender>>,
    ) -> Result<Response<Body>, Infallible> {
        if req.uri().path() == "/managed/config" {
            if let (Some(key), Some(controller)) = (&self.reload_key, &self.controller) {
                return Ok(controller.reload(req, key).await);
            }
        }
        let Some((port, key)) = &self.resources else {
            return self
                .handle(req)
                .await
                .map(|r| r.map(|b| b.map_err(|never| match never {}).boxed()));
        };
        let method = req.method().clone();
        let plan = http_dispatch_flow::plan(
            method.as_str().as_bytes(),
            req.uri().path().as_bytes(),
            req.headers()
                .get(AUTHORIZATION)
                .map_or(&[][..], HeaderValue::as_bytes),
            &self.token,
            cfg!(windows),
        );
        let error = |status, text: &'static str| {
            let data = if method == hyper::Method::HEAD {
                ""
            } else {
                text
            };
            Response::builder()
                .status(status)
                .header(CONTENT_TYPE, "text/plain; charset=utf-8")
                .header("X-Content-Type-Options", "nosniff")
                .header(CONTENT_LENGTH, text.len())
                .body(
                    Full::new(Bytes::from_static(data.as_bytes()))
                        .map_err(|never| match never {})
                        .boxed(),
                )
                .unwrap()
        };
        if plan.status == 401 {
            return Ok(error(401, "unauthorized\n"));
        }
        if plan.status == 404 {
            return Ok(error(404, "404 page not found\n"));
        }
        if plan.status != 0
            || !self.writes && !(method == hyper::Method::GET || method == hyper::Method::HEAD)
        {
            return Ok(error(405, "Method Not Allowed\n"));
        }
        // Only route IDs selected by the read dispatcher can reach the adapter.
        let mut path = format!("/{}", plan.route);
        if let Some(query) = req.uri().query() {
            path.push('?');
            path.push_str(query);
        }
        let mime = req.headers().get(CONTENT_TYPE).cloned();
        let mut body = Vec::new();
        if method != hyper::Method::GET && method != hyper::Method::HEAD {
            // The Go JSON decoder consumes at most 64 KiB. Wait for the complete
            // transport body before admitting any effect, including bodyless commands.
            let mut incoming = req.into_body();
            while let Some(frame) = incoming.frame().await {
                let Ok(frame) = frame else {
                    return Ok(error(400, "incomplete request body\n"));
                };
                if let Some(data) = frame.data_ref() {
                    let n = data.len().min(65536 - body.len());
                    body.extend_from_slice(&data[..n]);
                }
            }
        }
        let fetch = async {
            let mut builder = Request::builder()
                .method(method.clone())
                .uri(path)
                .header("Host", "localhost")
                .header(AUTHORIZATION, key);
            if let Some(mime) = mime {
                builder = builder.header(CONTENT_TYPE, mime);
            }
            let out = builder.body(Full::new(Bytes::from(body))).ok()?;
            // One adapter connection per ingress connection. Reconnect only
            // before dispatch; a failed send may already have applied a write.
            let mut slot = adapter.lock().await;
            let mut send = match slot.take() {
                Some(sender) if sender.is_ready() => sender,
                _ => {
                    let stream = TcpStream::connect((std::net::Ipv4Addr::LOCALHOST, *port))
                        .await
                        .ok()?;
                    stream.set_nodelay(true).ok()?;
                    let (sender, conn) =
                        hyper::client::conn::http1::handshake(TokioIo::new(stream))
                            .await
                            .ok()?;
                    tokio::spawn(async move {
                        let _ = conn.await;
                    });
                    sender
                }
            };
            let res = send.send_request(out).await.ok()?;
            *slot = Some(send);
            Some(res.map(BodyExt::boxed))
        };
        Ok(fetch
            .await
            .unwrap_or_else(|| error(502, "read resource unavailable\n")))
    }
}

impl Controller {
    async fn reload(&self, req: Request<Incoming>, key: &HeaderValue) -> Response<Body> {
        let empty = |status| {
            Response::builder()
                .status(status)
                .body(
                    Full::new(Bytes::new())
                        .map_err(|never| match never {})
                        .boxed(),
                )
                .unwrap()
        };
        if req.method() != hyper::Method::PUT {
            return empty(405);
        }
        if req.headers().get(AUTHORIZATION) != Some(key) {
            return empty(401);
        }
        // The caller supplies only a config payload; this session owns the fixed
        // controller address, method, path and credential for this generation.
        let send = async {
            let stream = TcpStream::connect((std::net::Ipv4Addr::LOCALHOST, self.port))
                .await
                .ok()?;
            let (mut sender, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
                .await
                .ok()?;
            tokio::spawn(async move {
                let _ = conn.await;
            });
            let out = Request::put("/configs")
                .header("Host", "localhost")
                .header(AUTHORIZATION, self.auth.clone())
                .header(CONTENT_TYPE, "application/json")
                .header("Connection", "close")
                .body(req.into_body())
                .ok()?;
            sender
                .send_request(out)
                .await
                .ok()
                .map(|r| r.map(BodyExt::boxed))
        };
        timeout(Duration::from_secs(5), send)
            .await
            .ok()
            .flatten()
            .unwrap_or_else(|| empty(502))
    }
}
