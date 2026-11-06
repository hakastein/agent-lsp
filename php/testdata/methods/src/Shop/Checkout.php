<?php

namespace Shop;

class Checkout
{
    public function run(): int
    {
        $cart = new Cart();
        $cart->add('apple');
        $cart->ADD('pear');
        return $cart->total();
    }

    public function wish(): void
    {
        $cart = new Wishlist();
        $cart->add('plum');
    }

    public function guess($cart): void
    {
        $cart->add('fig');
    }
}
