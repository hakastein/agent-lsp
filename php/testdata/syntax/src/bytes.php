<?php

namespace Shop;

class Bytes
{
    public const RAW = b'bin';

    public function pack(string $y): string
    {
        return b"x{$y}";
    }
}

function bytes(string $y): void
{
    $c = new Cart();
    $c->add(b'bin');
    $c->add(b"x{$y}");
    (new Bytes())->pack($y);
}
