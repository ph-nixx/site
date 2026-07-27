use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

#[derive(Serialize, Deserialize, Debug, PartialEq, Clone)]
pub struct RepoState {
    pub id: u64,
    pub name: String,
    pub language: Option<String>,
    pub description: Option<String>,
    pub head_commit: Option<Commit>,
}

#[derive(Deserialize, Debug)]
pub struct Repo {
    pub id: u64,
    pub name: String,
    pub full_name: String,
    pub description: Option<String>,
    pub language: Option<String>,
}

#[derive(Serialize, Deserialize, Debug, PartialEq, Clone)]
pub struct Commit {
    pub id: String,
    #[serde(default)]
    pub repo_name: String,
    pub timestamp: DateTime<Utc>,
    pub author: Author,
    pub distinct: bool,
    pub message: String,
    pub added: Vec<String>,
    pub modified: Vec<String>,
    pub removed: Vec<String>,
}

#[derive(Serialize, Deserialize, Debug, PartialEq, Clone)]
pub struct Author {
    pub username: String,
    pub email: String,
}

/// The convention for a section md file or child directory in `docs/`.
#[derive(Serialize, Deserialize, Clone)]
pub struct Section {
    pub title: Option<String>,
    pub source: Option<String>,
    pub items: Option<Vec<Section>>,
    pub html: Option<String>,
}
