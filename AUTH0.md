# Auth0 fork

## Branching

- `master`: contains the upstream `master` code
- `auth0-master`: contains the upstream `master` code plus our build related files
- `a0-fix-logging`: branched off of `master` which contains a fix by us.
- `auth0-fix-logging`: branched off of `auth0-master` which contains a fix by us plus our build related files.

To work on a fix or new feature that should eventually go back into upstream `master`, do the following:

```sh
git checkout master
# Create a new branch for your fix or feature, starting with a0.
git checkout -b a0-<fix-or-feature-name>
# Develop your fix or feature in this branch.
...

git checkout auth0-master
# Create a new branch for to build your fix or feature, starting with auth0.
git checkout -b auth0-<fix-or-feature-name>
git merge a0-<fix-or-feature-name>
# This branch will contain the fix or feature and our build related files.
```

## Links

- [Public repo](https://github.com/pusher/faros)
- [Our fork](https://github.com/auth0/faros)