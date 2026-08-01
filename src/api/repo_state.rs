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
    pub default_branch: String,
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
///
/// * html: raw html string parsed from the source md
/// * headings: extracted heading hierarchy in source order
#[derive(Serialize, Deserialize, Clone)]
pub struct Section {
    pub title: Option<String>,
    pub source: Option<String>,
    pub items: Option<Vec<Section>>,
    pub html: Option<String>,
    pub headings: Option<Vec<Heading>>,
}

#[derive(Serialize, Deserialize, Clone)]
pub struct Heading {
    pub text: String,
    pub slug: String,
    pub items: Option<Vec<Heading>>,
}
