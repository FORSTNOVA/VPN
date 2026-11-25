use crate::{dialer::BoxedStream, router::Router, rule::RuleAction};
use std::{
    collections::BTreeMap,
    io,
    net::SocketAddr,
    pin::Pin,
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering::Relaxed},
        Arc, Mutex,
    },
    task::{Context, Poll},
    time::Instant,
};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};

pub struct FlowCounters {
    pub id: u64,
    pub app: Option<String>,
    pub target: String,
    pub network: &'static str,
    pub route: String,
    pub started: Instant,
    pub upload: AtomicU64,
    pub download: AtomicU64,
    pub closed: AtomicBool,
}

#[derive(Default)]
pub struct Traffic {
    enabled: AtomicBool,
    next: AtomicU64,
    flows: Mutex<BTreeMap<u64, Arc<FlowCounters>>>,
}
impl Traffic {
    pub fn enable(&self) {
        self.enabled.store(true, Relaxed);
    }
    pub fn snapshot(&self) -> Vec<Arc<FlowCounters>> {
        self.flows.lock().unwrap().values().cloned().collect()
    }
    pub fn acknowledge(&self, completed: &[u64]) {
        let mut flows = self.flows.lock().unwrap();
        for id in completed {
            flows.remove(id);
        }
    }
}

pub(crate) struct Meter(Option<Arc<FlowCounters>>);
impl Meter {
    pub fn open(
        router: &Router,
        src: SocketAddr,
        dst: SocketAddr,
        udp: bool,
        target: String,
        action: &RuleAction,
    ) -> io::Result<Self> {
        if !router.traffic.enabled.load(Relaxed) {
            return Ok(Self(None));
        }
        let app = match &router.process_resolver {
            Some(resolver) => resolver.try_resolve(src, dst, udp)?,
            None => None,
        };
        let id = router.traffic.next.fetch_add(1, Relaxed);
        let flow = Arc::new(FlowCounters {
            id,
            app,
            target,
            network: if udp { "udp" } else { "tcp" },
            route: match action {
                RuleAction::Direct => "DIRECT".into(),
                RuleAction::Reject => "REJECT".into(),
                RuleAction::Proxy(name) => name.clone(),
            },
            started: Instant::now(),
            upload: 0.into(),
            download: 0.into(),
            closed: false.into(),
        });
        router
            .traffic
            .flows
            .lock()
            .unwrap()
            .insert(id, flow.clone());
        Ok(Self(Some(flow)))
    }
    pub fn up(&self, n: usize) {
        if let Some(flow) = &self.0 {
            flow.upload.fetch_add(n as u64, Relaxed);
        }
    }
    pub fn down(&self, n: usize) {
        if let Some(flow) = &self.0 {
            flow.download.fetch_add(n as u64, Relaxed);
        }
    }
}
impl Drop for Meter {
    fn drop(&mut self) {
        if let Some(flow) = &self.0 {
            flow.closed
                .store(true, std::sync::atomic::Ordering::Release);
        }
    }
}
pub(crate) struct MeteredStream(pub BoxedStream, pub Meter);
impl AsyncRead for MeteredStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let start = buf.filled().len();
        let result = Pin::new(&mut self.0).poll_read(cx, buf);
        if let Poll::Ready(Ok(())) = result {
            self.1.down(buf.filled().len() - start);
        }
        result
    }
}
impl AsyncWrite for MeteredStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        let result = Pin::new(&mut self.0).poll_write(cx, buf);
        if let Poll::Ready(Ok(n)) = result {
            self.1.up(n);
        }
        result
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_shutdown(cx)
    }
}
