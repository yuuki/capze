capze
======

`capze` is a tool to make [Capistrano](http://capistranorb.com/)-like directory structure.

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
$ capze --keep 5 /tmp/app /var/www/app

$ capze --rollback /var/www/app

$ capze --pruned-dirs --keep 1 /var/www/app
/var/www/app/releases/20161006002523
/var/www/app/releases/20170621124528
```

You can use `capze` in combination with [Droot](https://github.com/yuuki/droot).

```bash
$ aws s3 cp s3://drootexamples/app.tar.gz - | tar xzf - -C /tmp/app
$ capze --keep 5 /tmp/app /var/www/app
```

## License
MIT
