use serde::Deserialize;
use smartvpn_store::{
    traffic::{TrafficHistoryReport, TrafficSample},
    Store,
};
#[derive(Deserialize)]
struct Step {
    session: String,
    bucket: String,
    now: i64,
    samples: Vec<TrafficSample>,
}
#[derive(Deserialize)]
struct Input {
    steps: Vec<Step>,
    queries: Vec<(TrafficHistoryReport, String)>,
}
fn main() {
    let path = std::env::args().nth(1).expect("temporary database path");
    let input: Input = serde_json::from_reader(std::io::stdin()).unwrap();
    let store = Store::open(path).unwrap();
    for step in input.steps {
        store
            .record_traffic(&step.session, &step.samples, &step.bucket, step.now)
            .unwrap();
    }
    let reports: Vec<_> = input
        .queries
        .into_iter()
        .map(|(r, key)| store.read_traffic_history(r, &key).unwrap())
        .collect();
    println!("{}", serde_json::to_string(&reports).unwrap());
}
