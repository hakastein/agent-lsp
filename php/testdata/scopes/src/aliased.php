<?php

use Shop\Cart;
use Shop\Wishlist;

$aliased = new Cart();
$r = new Wishlist();
$aliased->add('alias-from-another-file');
