macro_rules! vocabulary {
    ($name:ident { $($konst:ident = $value:literal),* $(,)? }) => {
        #[derive(
            Clone, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash,
            serde::Serialize, serde::Deserialize,
        )]
        #[serde(transparent)]
        pub struct $name(std::borrow::Cow<'static, str>);

        impl $name {
            $(pub const $konst: Self = Self::from_static($value);)*

            pub const fn from_static(name: &'static str) -> Self {
                Self(std::borrow::Cow::Borrowed(name))
            }

            pub fn as_str(&self) -> &str {
                &self.0
            }

            pub fn is_empty(&self) -> bool {
                self.0.is_empty()
            }
        }

        impl From<String> for $name {
            fn from(name: String) -> Self {
                Self(std::borrow::Cow::Owned(name))
            }
        }

        impl From<&str> for $name {
            fn from(name: &str) -> Self {
                Self(std::borrow::Cow::Owned(name.to_owned()))
            }
        }

        impl PartialEq<str> for $name {
            fn eq(&self, other: &str) -> bool {
                self.as_str() == other
            }
        }

        impl PartialEq<&str> for $name {
            fn eq(&self, other: &&str) -> bool {
                self.as_str() == *other
            }
        }

        impl std::fmt::Display for $name {
            fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                f.write_str(&self.0)
            }
        }
    };
}

pub(crate) use vocabulary;
