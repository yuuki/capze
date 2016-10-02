capze
======

`capze` is a tool to make [Capistrano](http://capistranorb.com/)-lile directory structure.

```bash
$ tree -L 2 /var/www/app
/var/www/app
├── current -> /var/www/app/releases/20161002074731
└── releases
    ├── 20161002074709
    ├── 20161002074713
    ├── 20161002074716
    ├── 20161002074731
    └── 20161002081856
```

## Usage

```bash
$ capze --keeps 5 /tmp/app /var/www/app

$ capze --rollback /var/www/app
```

You can use `capze` in combination with [Droot](https://github.com/yuuki/droot).

```bash
$ aws s3 cp s3://drootexamples/app.tar.gz - | tar xz -C /tmp/app
$ capze --keeps 5 /tmp/app /var/www/app
```
